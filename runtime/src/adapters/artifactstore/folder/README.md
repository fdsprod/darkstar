# Folder artifact store

The folder adapter implements `ports/artifactstore.Store` with immutable
SHA-256-addressed files below one configured absolute directory. Callers receive
only `sha256:<digest>` locators and never depend on this layout.
When configuration does not supply a root, `ResolveRoot` selects the MVP default
at `<project>/.darkstar/artifacts`.

```text
<root>/
  blobs/sha256/<prefix>/<digest>
  metadata/sha256/<prefix>/<digest>.json
  operations/sha256/<prefix>/<idempotency-key-hash>.json
  .tmp/
```

Blob files contain the original bytes without an envelope, so ordinary tools
can read or copy them. Metadata contains only values that cannot be derived from
the bytes (the first accepted media-type hint and storage time). Operation
records keep retry identity separate from content identity; multiple operations
and later artifact records may refer to the same immutable blob.

Writes stream into a same-volume temporary file, flush it, and atomically publish
it under the computed digest. Metadata is the visibility commit point. A crash
before that point can leave only an unlisted content-addressed blob, which a
retry safely verifies and adopts.
