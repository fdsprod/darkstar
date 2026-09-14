# FEATURE\-1 story backlog

> Derived planning projection. Approval and business completion are separate records.

## Stories

### STORY\-1: Author a feature brief

**Lifecycle:** active

An owner can review intent

- **Acceptance Criteria**
  - Requirements retain stable keys

- **Scope**
  - **Included**
    - Brief authoring
  - **Excluded**
    - Ticket mutation

- **Requirement Keys**
  - REQ\-1

- **Repository Impact**
  - **Kind:** none
  - **Rationale:** Planning needs no source changes

- **Dependencies**: None

- **Evidence Keys**
  - E\-2

- **Decision Keys**
  - D\-2

- **Lifecycle**
  - **Kind:** active

### STORY\-2: Investigate publication

**Lifecycle:** active

An owner can review intent

- **Acceptance Criteria**
  - Requirements retain stable keys

- **Scope**
  - **Included**
    - Brief authoring
  - **Excluded**
    - Ticket mutation

- **Requirement Keys**
  - REQ\-1

- **Repository Impact**
  - **Kind:** unknown
  - **Reason:** Investigation pending

- **Dependencies**
  - STORY\-1

- **Evidence Keys**
  - E\-2

- **Decision Keys**
  - D\-2

- **Lifecycle**
  - **Kind:** active

## Evidence

- **Key:** E\-2
- **Artifact**
  - **Artifact Id:** artifact\-evidence
  - **Version:** 1
  - **Sha256:** eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee
- **Locator:** section 3
- **Finding:** Explicit publication configuration is required\.

## Decisions

- **Key:** D\-2
- **Question:** Does publication require code changes?
- **Evidence Keys**
  - E\-2
- **State**
  - **Kind:** open
  - **Blocking:** true

## Provenance

- **Schema Version:** 1
- **Project Id:** project\-factory
- **Feature Key:** FEATURE\-1
- **Artifact Type:** story\_backlog
- **Lineage**
  - **Kind:** initial
- **Brief**
  - **Artifact Id:** artifact\-brief
  - **Version:** 1
  - **Sha256:** bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
