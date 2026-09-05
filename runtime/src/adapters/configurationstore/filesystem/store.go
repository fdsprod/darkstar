// Package filesystem persists configuration YAML with compare-and-swap and recovery.
package filesystem

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"darkstar/src/daemon/configuration"
	"darkstar/src/ports/configurationstore"
	"go.yaml.in/yaml/v3"
)

const absentRevisionSeed = "darkstar:configuration:absent:v1"

type Store struct {
	mu                sync.Mutex
	userPath          string
	projectPath       string
	secretPath        string
	recoveryDirectory string
}

func New(locations configuration.FileLocations, dataDirectory string) (*Store, error) {
	for label, value := range map[string]string{"user configuration": locations.UserConfig, "project configuration": locations.ProjectConfig, "secret configuration": locations.UserSecrets, "data directory": dataDirectory} {
		if !filepath.IsAbs(value) {
			return nil, fmt.Errorf("%s path must be absolute: %q", label, value)
		}
	}
	return &Store{userPath: filepath.Clean(locations.UserConfig), projectPath: filepath.Clean(locations.ProjectConfig), secretPath: filepath.Clean(locations.UserSecrets), recoveryDirectory: filepath.Join(filepath.Clean(dataDirectory), "configuration-recovery")}, nil
}

func (s *Store) Snapshot(ctx context.Context, target configurationstore.Target) (configurationstore.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return configurationstore.Snapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot(s.path(target))
}

func (s *Store) SecretRevision(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	content, present, err := readBounded(s.secretPath)
	if err != nil {
		return "", err
	}
	return revision(content, present), nil
}

func (s *Store) Preview(ctx context.Context, target configurationstore.Target, mutation configurationstore.Mutation) (configurationstore.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return configurationstore.Snapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.path(target)
	content, present, err := readBounded(path)
	if err != nil {
		return configurationstore.Snapshot{}, err
	}
	candidate, values, err := mutate(content, mutation)
	if err != nil {
		return configurationstore.Snapshot{}, err
	}
	return configurationstore.Snapshot{Revision: revision(candidate, true), Present: present || len(candidate) != 0, Reference: path, Values: values}, nil
}

func (s *Store) Apply(ctx context.Context, target configurationstore.Target, mutation configurationstore.Mutation, expected string) (configurationstore.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return configurationstore.Snapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.path(target)
	content, present, err := readBounded(path)
	if err != nil {
		return configurationstore.Snapshot{}, err
	}
	if revision(content, present) != expected {
		return configurationstore.Snapshot{}, fmt.Errorf("%w: expected %s, current %s", configurationstore.ErrRevisionConflict, expected, revision(content, present))
	}
	candidate, values, err := mutate(content, mutation)
	if err != nil {
		return configurationstore.Snapshot{}, err
	}
	if len(candidate) > configuration.MaxFileSize {
		return configurationstore.Snapshot{}, fmt.Errorf("configuration exceeds %d bytes", configuration.MaxFileSize)
	}
	if err := s.writeRecoverable(path, content, present, candidate, targetName(target)); err != nil {
		return configurationstore.Snapshot{}, err
	}
	return configurationstore.Snapshot{Revision: revision(candidate, true), Present: true, Reference: path, Values: values}, nil
}

func (s *Store) Restore(ctx context.Context, target configurationstore.Target, expected string) (configurationstore.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return configurationstore.Snapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.path(target)
	current, present, err := readBounded(path)
	if err != nil {
		return configurationstore.Snapshot{}, err
	}
	if revision(current, present) != expected {
		return configurationstore.Snapshot{}, fmt.Errorf("%w: expected %s, current %s", configurationstore.ErrRevisionConflict, expected, revision(current, present))
	}
	backupPath := filepath.Join(s.recoveryDirectory, targetName(target)+".previous.json")
	backup, err := readBackup(backupPath)
	if errors.Is(err, os.ErrNotExist) {
		return configurationstore.Snapshot{}, configurationstore.ErrNoPrevious
	}
	if err != nil {
		return configurationstore.Snapshot{}, err
	}
	if err := publish(path, backup.Content, backup.Present, target == configurationstore.TargetUser); err != nil {
		return configurationstore.Snapshot{}, err
	}
	values, err := decodeValues(backup.Content, backup.Present)
	if err != nil {
		return configurationstore.Snapshot{}, err
	}
	return configurationstore.Snapshot{Revision: revision(backup.Content, backup.Present), Present: backup.Present, Reference: path, Values: values}, nil
}

func (s *Store) PutSecret(ctx context.Context, name, value, expected string) (configurationstore.SecretReceipt, error) {
	if err := ctx.Err(); err != nil {
		return configurationstore.SecretReceipt{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, present, err := readBounded(s.secretPath)
	if err != nil {
		return configurationstore.SecretReceipt{}, err
	}
	if revision(current, present) != expected {
		return configurationstore.SecretReceipt{}, fmt.Errorf("%w: expected %s, current %s", configurationstore.ErrRevisionConflict, expected, revision(current, present))
	}
	candidate, _, err := mutate(current, configurationstore.Mutation{Operation: configurationstore.OperationSet, Path: []string{"secrets", name}, Value: value})
	if err != nil {
		return configurationstore.SecretReceipt{}, err
	}
	if err := s.writeRecoverable(s.secretPath, current, present, candidate, "secrets"); err != nil {
		return configurationstore.SecretReceipt{}, err
	}
	return configurationstore.SecretReceipt{Revision: revision(candidate, true), Name: name}, nil
}

func (s *Store) snapshot(path string) (configurationstore.Snapshot, error) {
	content, present, err := readBounded(path)
	if err != nil {
		return configurationstore.Snapshot{}, err
	}
	values, err := decodeValues(content, present)
	if err != nil {
		return configurationstore.Snapshot{}, err
	}
	return configurationstore.Snapshot{Revision: revision(content, present), Present: present, Reference: path, Values: values}, nil
}

func (s *Store) path(target configurationstore.Target) string {
	if target == configurationstore.TargetProject {
		return s.projectPath
	}
	return s.userPath
}

type backupDocument struct {
	Present bool   `json:"present"`
	Content []byte `json:"content"`
}

func (s *Store) writeRecoverable(path string, previous []byte, previousPresent bool, candidate []byte, label string) error {
	if err := verifyBoundary(path); err != nil {
		return err
	}
	if err := os.MkdirAll(s.recoveryDirectory, 0o700); err != nil {
		return fmt.Errorf("create configuration recovery directory: %w", err)
	}
	encoded, err := json.Marshal(backupDocument{Present: previousPresent, Content: previous})
	if err != nil {
		return err
	}
	if len(encoded) > configuration.MaxFileSize+1024 {
		return errors.New("configuration recovery snapshot is too large")
	}
	if err := atomicReplace(filepath.Join(s.recoveryDirectory, label+".previous.json"), encoded, true); err != nil {
		return fmt.Errorf("save previous configuration: %w", err)
	}
	if err := publish(path, candidate, true, true); err != nil {
		return err
	}
	return nil
}

func publish(path string, content []byte, present, userOnly bool) error {
	if err := verifyBoundary(path); err != nil {
		return err
	}
	if !present {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove restored configuration: %w", err)
		}
		return nil
	}
	return atomicReplace(path, content, userOnly)
}

func atomicReplace(path string, content []byte, userOnly bool) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create configuration directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".darkstar-config-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if userOnly {
		err = temporary.Chmod(0o600)
	}
	if err == nil {
		_, err = temporary.Write(content)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write temporary configuration: %w", err)
	}
	if err := replacePublishedFile(temporaryPath, path); err != nil {
		return fmt.Errorf("publish configuration: %w", err)
	}
	return nil
}

func verifyBoundary(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%w: path is not absolute", configurationstore.ErrPathBoundary)
	}
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil && (info.Mode()&os.ModeSymlink != 0 || isReparsePoint(info)) {
			return fmt.Errorf("%w: symbolic link at %s", configurationstore.ErrPathBoundary, current)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect configuration boundary: %w", err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return nil
}

func readBounded(path string) ([]byte, bool, error) {
	if err := verifyBoundary(path); err != nil {
		return nil, false, err
	}
	staged := path + ".replacing"
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		if _, stagedErr := os.Lstat(staged); stagedErr == nil {
			if renameErr := os.Rename(staged, path); renameErr != nil {
				return nil, false, fmt.Errorf("recover staged configuration: %w", renameErr)
			}
		}
	} else if err == nil {
		_ = os.Remove(staged)
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, configuration.MaxFileSize+1))
	if err != nil {
		return nil, false, err
	}
	if len(content) > configuration.MaxFileSize {
		return nil, false, fmt.Errorf("configuration exceeds %d bytes", configuration.MaxFileSize)
	}
	return content, true, nil
}

func decodeValues(content []byte, present bool) (map[string]any, error) {
	if !present || len(bytes.TrimSpace(content)) == 0 {
		return map[string]any{}, nil
	}
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	values := map[string]any{}
	if err := decoder.Decode(&values); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple YAML documents are not supported")
		}
		return nil, err
	}
	return values, nil
}

func mutate(content []byte, mutation configurationstore.Mutation) ([]byte, map[string]any, error) {
	if len(mutation.Path) == 0 {
		return nil, nil, errors.New("configuration mutation path is required")
	}
	for _, segment := range mutation.Path {
		if strings.TrimSpace(segment) == "" {
			return nil, nil, errors.New("configuration mutation path contains an empty segment")
		}
	}
	var document yaml.Node
	if len(bytes.TrimSpace(content)) == 0 {
		document = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	} else if err := yaml.Unmarshal(content, &document); err != nil {
		return nil, nil, err
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, nil, errors.New("configuration document must be a mapping")
	}
	if err := mutateNode(document.Content[0], mutation.Path, mutation); err != nil {
		return nil, nil, err
	}
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return nil, nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, nil, err
	}
	values, err := decodeValues(output.Bytes(), true)
	if err != nil {
		return nil, nil, err
	}
	return output.Bytes(), values, nil
}

func mutateNode(mapping *yaml.Node, path []string, mutation configurationstore.Mutation) error {
	key := path[0]
	for index := 0; index < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value != key {
			continue
		}
		if len(path) == 1 {
			if mutation.Operation == configurationstore.OperationUnset {
				mapping.Content = append(mapping.Content[:index], mapping.Content[index+2:]...)
				return nil
			}
			valueNode := &yaml.Node{}
			if err := valueNode.Encode(mutation.Value); err != nil {
				return err
			}
			mapping.Content[index+1] = valueNode
			return nil
		}
		child := mapping.Content[index+1]
		if child.Kind != yaml.MappingNode {
			child = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			mapping.Content[index+1] = child
		}
		return mutateNode(child, path[1:], mutation)
	}
	if mutation.Operation == configurationstore.OperationUnset {
		return nil
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	if len(path) == 1 {
		valueNode := &yaml.Node{}
		if err := valueNode.Encode(mutation.Value); err != nil {
			return err
		}
		mapping.Content = append(mapping.Content, keyNode, valueNode)
		return nil
	}
	child := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	mapping.Content = append(mapping.Content, keyNode, child)
	return mutateNode(child, path[1:], mutation)
}

func revision(content []byte, present bool) string {
	if !present {
		sum := sha256.Sum256([]byte(absentRevisionSeed))
		return hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func targetName(target configurationstore.Target) string {
	if target == configurationstore.TargetProject {
		return "project"
	}
	return "user"
}

func readBackup(path string) (backupDocument, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return backupDocument{}, err
	}
	var value backupDocument
	if err := json.Unmarshal(content, &value); err != nil {
		return backupDocument{}, fmt.Errorf("decode configuration recovery snapshot: %w", err)
	}
	if len(value.Content) > configuration.MaxFileSize {
		return backupDocument{}, errors.New("configuration recovery snapshot is too large")
	}
	return value, nil
}

var _ configurationstore.Store = (*Store)(nil)
