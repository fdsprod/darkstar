package investigationrunner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"darkstar/src/core/investigation"
	"darkstar/src/ports/artifactregistry"
)

func digest(content []byte) string {
	value := sha256.Sum256(content)
	return hex.EncodeToString(value[:])
}

func decode(content []byte, target any) error {
	if len(content) == 0 || len(content) > MaxContentBytes || !utf8.Valid(content) {
		return errors.New("investigation content exceeds limits or is not UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("investigation content must contain one JSON value")
	}
	return nil
}

func (s *Service) ResolveTask(ctx context.Context, input investigation.TaskInput, projectID string) (investigation.FrozenTask, error) {
	result := investigation.FrozenTask{Kind: input.Kind}
	switch input.Kind {
	case "text":
		if input.FeatureBrief != nil || strings.TrimSpace(input.Text) == "" || len(input.Text) > MaxContentBytes/2 {
			return result, errors.New("text investigation requires a bounded nonempty task and no artifact reference")
		}
		result.Content, _ = json.Marshal(struct {
			Task string `json:"task"`
		}{input.Text})
	case "feature_brief":
		if input.Text != "" || input.FeatureBrief == nil || input.FeatureBrief.ArtifactID == "" || input.FeatureBrief.Version == 0 || len(input.FeatureBrief.SHA256) != 64 {
			return result, errors.New("feature brief requires an exact artifact version and SHA-256")
		}
		content, err := s.dependencies.Artifacts.OriginalContent(ctx, artifactregistry.VersionRef{ArtifactID: input.FeatureBrief.ArtifactID, Version: input.FeatureBrief.Version})
		if err != nil {
			return result, err
		}
		defer func() {
			_ = content.Reader.Close()
		}()
		result.Content, err = io.ReadAll(io.LimitReader(content.Reader, MaxContentBytes+1))
		if err != nil || len(result.Content) > MaxContentBytes || digest(result.Content) != input.FeatureBrief.SHA256 || content.Digest != input.FeatureBrief.SHA256 {
			return result, errors.New("feature brief bytes do not match the exact admitted digest or exceed limits")
		}
		var brief struct {
			ArtifactType string `json:"artifactType"`
			ProjectID    string `json:"projectId"`
		}
		if json.Unmarshal(result.Content, &brief) != nil || brief.ArtifactType != "feature_brief" || brief.ProjectID != projectID {
			return result, errors.New("feature brief type or project does not match this investigation")
		}
		if err := s.dependencies.ValidateBrief(ctx, projectID, result.Content); err != nil {
			return result, err
		}
		result.Content, err = json.Marshal(json.RawMessage(result.Content))
		if err != nil {
			return result, err
		}
		source := *input.FeatureBrief
		result.Source = &source
	default:
		return result, errors.New("unsupported investigation task kind")
	}
	result.Digest = digest(result.Content)
	return result, nil
}

func validateTaskScope(execution investigation.Execution) error {
	if digest(execution.Task.Content) != execution.Task.Digest {
		return errors.New("frozen investigation task digest changed")
	}
	if execution.Task.Kind != "feature_brief" {
		if execution.Task.Kind != "text" {
			return errors.New("unsupported frozen task kind")
		}
		return nil
	}
	var brief struct {
		ProjectID    string `json:"projectId"`
		Repositories []struct {
			RepositoryID       string `json:"repositoryId"`
			MembershipRevision uint64 `json:"membershipRevision"`
		} `json:"repositories"`
	}
	if json.Unmarshal(execution.Task.Content, &brief) != nil || brief.ProjectID != execution.ProjectID || execution.Task.Source == nil {
		return errors.New("frozen feature brief identity is invalid")
	}
	for _, entry := range execution.Scope.Repositories {
		matched := false
		for _, candidate := range brief.Repositories {
			if candidate.RepositoryID == entry.Repository.RepositoryID && candidate.MembershipRevision == entry.Membership.Revision {
				matched = true
			}
		}
		if !matched {
			return errors.New("selected repository is absent from the feature brief catalog or has a different membership revision")
		}
	}
	return nil
}

func textFields(quality, summary string, groups ...[]string) error {
	if (quality != "complete" && quality != "partial" && quality != "missing") || strings.TrimSpace(summary) == "" {
		return errors.New("findings require explicit quality and a summary")
	}
	for _, group := range groups {
		if group == nil || len(group) > 256 {
			return errors.New("findings arrays must be explicit and bounded")
		}
		for _, text := range group {
			if strings.TrimSpace(text) == "" || len(text) > 32768 {
				return errors.New("findings contain empty or oversized text")
			}
		}
	}
	if quality != "complete" && len(groups[len(groups)-1]) == 0 {
		return errors.New("partial or missing findings require explicit limitations")
	}
	return nil
}

func (s *Service) validateOutput(ctx context.Context, execution investigation.Execution, raw json.RawMessage) (investigation.UnitResult, error) {
	result := investigation.UnitResult{Kind: execution.Kind}
	if err := s.dependencies.Schema.Validate(OutputSchema(execution.Kind), raw); err != nil {
		return result, err
	}
	switch execution.Kind {
	case "repository":
		if len(execution.Scope.Repositories) != 1 || len(execution.Scope.Evidence) != 1 {
			return result, errors.New("repository attempt requires exactly one frozen evidence root")
		}
		var findings RepositoryFindings
		if err := decode(raw, &findings); err != nil {
			return result, err
		}
		entry, evidence := execution.Scope.Repositories[0], execution.Scope.Evidence[0]
		if findings.SchemaVersion != 1 || findings.RepositoryID != entry.Repository.RepositoryID || findings.CommitSHA != entry.Revision.CommitSHA || evidence.RepositoryID != findings.RepositoryID {
			return result, errors.New("findings repository and commit must match frozen evidence")
		}
		if err := textFields(findings.Quality, findings.Summary, findings.AffectedInterfaces, findings.ReusablePatterns, findings.Constraints, findings.Risks, findings.UnresolvedQuestions, findings.Limitations); err != nil {
			return result, err
		}
		if findings.CodeReferences == nil || len(findings.CodeReferences) > 256 || (findings.Quality == "complete" && len(findings.CodeReferences) == 0) || (findings.Quality == "missing" && len(findings.CodeReferences) != 0) {
			return result, errors.New("code evidence does not support declared findings quality")
		}
		manifest, err := s.dependencies.Evidence.Manifest(ctx, evidence.Evidence)
		if err != nil {
			return result, err
		}
		if manifest.Request.RepositoryID != findings.RepositoryID || manifest.Request.CommitSHA != findings.CommitSHA || manifest.TreeSHA != entry.Revision.TreeSHA {
			return result, errors.New("retained manifest does not match frozen repository identity")
		}
		if len(manifest.Exclusions) != 0 && findings.Quality == "complete" {
			return result, errors.New("excluded repository content requires partial findings with explicit limitations")
		}
		for _, citation := range findings.CodeReferences {
			if citation.RepositoryID != findings.RepositoryID || citation.CommitSHA != findings.CommitSHA || citation.StartLine < 1 || citation.EndLine < citation.StartLine {
				return result, errors.New("code reference is outside the selected repository revision")
			}
			found := false
			for _, file := range manifest.Files {
				if file.Path == citation.Path && file.BlobSHA == citation.BlobSHA {
					found = true
				}
			}
			if !found {
				return result, errors.New("code reference does not identify a verified manifest file/blob")
			}
			content, err := s.dependencies.Evidence.ReadFile(ctx, evidence.Evidence, citation.Path)
			if err != nil {
				return result, err
			}
			lines := bytes.Count(content, []byte{'\n'})
			if len(content) > 0 && content[len(content)-1] != '\n' {
				lines++
			}
			if !utf8.Valid(content) || bytes.ContainsRune(content, 0) || citation.EndLine > lines {
				return result, errors.New("code reference line range is outside the verified text file")
			}
		}
		result.RepositoryID, result.Quality = findings.RepositoryID, findings.Quality
		result.Findings, _ = json.Marshal(findings)
	case "synthesis":
		if len(execution.Scope.Repositories) != 0 || len(execution.Scope.Evidence) != 0 {
			return result, errors.New("synthesis cannot receive repository roots")
		}
		var synthesis Synthesis
		if err := decode(raw, &synthesis); err != nil {
			return result, err
		}
		if synthesis.SchemaVersion != 1 || synthesis.Findings == nil || synthesis.Missing == nil {
			return result, errors.New("synthesis requires exact findings and missing evidence arrays")
		}
		if err := textFields(synthesis.Quality, synthesis.Summary, synthesis.CrossRepositoryImplications, synthesis.Constraints, synthesis.Risks, synthesis.UnresolvedQuestions, synthesis.Limitations); err != nil {
			return result, err
		}
		expected := make([]FindingReference, 0, len(execution.Findings))
		partial := len(execution.Missing) > 0
		hasCodeEvidence := false
		for _, finding := range execution.Findings {
			if err := s.verifyResult(ctx, finding); err != nil {
				return result, err
			}
			expected = append(expected, FindingReference{RepositoryID: finding.RepositoryID, ArtifactID: finding.Artifact.ArtifactID, Version: finding.Artifact.Version, Digest: finding.Digest})
			partial = partial || finding.Quality != "complete"
			var accepted RepositoryFindings
			if err := json.Unmarshal(finding.Findings, &accepted); err != nil {
				return result, err
			}
			hasCodeEvidence = hasCodeEvidence || len(accepted.CodeReferences) > 0
		}
		sort.Slice(expected, func(i, j int) bool {
			return expected[i].RepositoryID < expected[j].RepositoryID
		})
		sort.Slice(synthesis.Findings, func(i, j int) bool {
			return synthesis.Findings[i].RepositoryID < synthesis.Findings[j].RepositoryID
		})
		missing := append([]investigation.MissingUnit{}, execution.Missing...)
		sort.Slice(missing, func(i, j int) bool {
			return missing[i].RepositoryID < missing[j].RepositoryID
		})
		sort.Slice(synthesis.Missing, func(i, j int) bool {
			return synthesis.Missing[i].RepositoryID < synthesis.Missing[j].RepositoryID
		})
		if !reflect.DeepEqual(expected, synthesis.Findings) || !reflect.DeepEqual(missing, synthesis.Missing) {
			return result, errors.New("synthesis must cite every exact accepted finding and preserve every missing unit")
		}
		if (!hasCodeEvidence && synthesis.EvidenceStatus != "no_repository_evidence") || (hasCodeEvidence && synthesis.EvidenceStatus != "repository_evidence") || ((partial || !hasCodeEvidence) && synthesis.Quality == "complete") {
			return result, errors.New("synthesis coverage does not match retained evidence")
		}
		result.Quality = synthesis.Quality
		result.Findings, _ = json.Marshal(synthesis)
	default:
		return result, errors.New("unsupported investigation unit kind")
	}
	result.Digest = digest(result.Findings)
	return result, nil
}

func (s *Service) verifyResult(ctx context.Context, result investigation.UnitResult) error {
	if err := s.dependencies.Schema.Validate(OutputSchema(result.Kind), result.Findings); err != nil {
		return err
	}
	var identity struct {
		RepositoryID string `json:"repositoryId"`
		Quality      string `json:"quality"`
	}
	if json.Unmarshal(result.Findings, &identity) != nil || identity.RepositoryID != result.RepositoryID || identity.Quality != result.Quality {
		return errors.New("retained findings metadata differs from artifact content")
	}
	if result.Artifact.ArtifactID == "" || result.Artifact.Version == 0 || digest(result.Findings) != result.Digest {
		return errors.New("accepted finding lacks an exact artifact identity and digest")
	}
	content, err := s.dependencies.Artifacts.OriginalContent(ctx, result.Artifact)
	if err != nil {
		return err
	}
	defer func() {
		_ = content.Reader.Close()
	}()
	raw, err := io.ReadAll(io.LimitReader(content.Reader, MaxContentBytes+1))
	if err != nil || !bytes.Equal(raw, result.Findings) || content.Digest != result.Digest {
		return errors.New("accepted findings artifact bytes or digest changed")
	}
	return nil
}
