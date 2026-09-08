# CSV Structured Metadata / CSV Document — Implementation v0.1

This document records how CSV Structured Metadata v0.1 is implemented in this
repository. The implementation stays inside the existing CSV Parser / CSV
Collector application layer; no Runtime Kernel, Fiber lifecycle, Plugin
Composition, or UI Composition changes are introduced.

## 1. Model

A configured CSV is no longer only `Header + Rows`. The parser returns a
Document:

```text
CSVDocument
├── Metadata        key/value pairs from the Metadata Section
└── DataSet         Header + streaming Records
```

The Go contract is `model.CSVDocument` plus the existing `RecordStream`:

```go
type CSVDocument struct {
    Metadata   Metadata
    Structured bool
    Data       RecordStream
}
```

`Structured=true` means the file explicitly declared and parsed a Metadata
Section. Ordinary CSV without the structured configuration keeps
`Metadata=empty`, `Structured=false`, and the existing Collector behavior.

## 2. Configuration

The layout is declared explicitly on the `csv-parser` component. Row numbers
are physical rows starting at 1:

```toml
[[components]]
id = "csv-parser"
type = "csv-parser"

[components.config]
header = true

[components.config.csv.metadata]
mode = "key_value"
start_row = 1
end_row = 5

[components.config.csv.data]
header_row = 7
```

For the example file:

```text
1  设备名称,ABC-001
2  设备型号,XYZ-200
3  产线,Line-01
4  工位,Station-03
5  采集时间,2026-09-08 10:30:00
6
7  参数,数值,单位
8  温度,23.5,℃
9  压力,1.02,MPa
```

Rows `1..5` are parsed as metadata and never become data records. Row `6` is a
blank separator. Row `7` is the Data Header and rows after it are data
records. `mode = "none"` (the default when the `csv` table is absent) keeps
the legacy `CSV -> RecordStream` behavior.

## 3. Validation

Application configuration rejects before Runtime mutation:

```text
start_row < 1
end_row < start_row
header_row <= end_row
unknown metadata mode
structured mode combined with header=false
structured mode combined with skip_lines
```

The parser also rejects invalid CSV documents:

```text
empty metadata key
duplicate metadata key
metadata row that is not exactly key,value
non-blank rows between Metadata Section and Data Header
duplicate data header column
data header missing / out of bounds
```

Parser errors carry the physical row and the Collector wraps them with the
source and file name, satisfying the CM-19 location requirement.

## 4. Metadata semantics

Metadata values are strings. Leading/trailing whitespace is trimmed from the
key for duplicate detection; values are preserved exactly. Keys are not
type-converted: `001` remains `"001"` and dates remain strings.

Once a CSV Document is structured, Business Metadata is merged with an
explicit namespace:

```text
path.*    path/filename metadata (existing rules)
csv.*     metadata from the CSV Metadata Section
```

`path.station = Station-03` and `csv.station = Station-04` therefore cannot
overwrite each other. For compatibility, legacy path metadata keys remain
available under their original rule names when the CSV is ordinary; when a
Document is structured, the same values are also exposed as `path.*`.

## 5. Boundary

The parser owns CSV syntax, Metadata Section extraction, and Data Section
extraction. It does not own storage, scheduler, Runtime lifecycle, WAL, or UI.
CSV metadata never participates in `FileIdentity.Identity()`; the existing
file identity and file-level idempotency semantics remain unchanged.

The Metadata Section belongs to the CSV Parser capability. No new Runtime CSV
Metadata Component or capability is introduced.

## 6. Test coverage

| Requirement | Test |
| --- | --- |
| CM-01 Basic Metadata | `TestCM01BasicStructuredMetadata` |
| CM-02 Multiple Fields | same test |
| CM-03 Empty Value | `TestCM03EmptyMetadataValue` |
| CM-04 Duplicate Metadata Key | `TestCM04DuplicateMetadataKey` |
| CM-05 Empty Metadata Key | `TestCM05EmptyMetadataKey` |
| CM-06 Malformed Metadata Row | `TestCM06MalformedMetadataRow` |
| CM-07 Blank Separator | `TestCM07BlankSeparatorAndCM08HeaderPosition` |
| CM-08 Header Position | same test |
| CM-09 Duplicate Data Header | `TestCM09DuplicateDataHeader` |
| CM-10 Namespace Collision | `TestCM10MetadataDataNamespaceCollision` |
| CM-11 Path + CSV Merge | `TestCM11PathAndCSVMetadataMerge` |
| CM-12 Multiple Sources | `TestCM12IndependentParserLayoutsDoNotLeak` + path metadata multi-source tests |
| CM-13 Reload | `TestCM13ReloadAndCM14InvalidReloadKeepsCurrentParser` |
| CM-14 Invalid Reload | same test |
| CM-15 Backward Compatibility | `TestCM15BackwardCompatibilityNoMetadataConfig` |
| CM-16 Quoting | `TestCM16CSVQuotingInMetadata` |
| CM-17 Idempotency | `TestCM20StructuredMetadataE2EToStorage` (rerun) |
| CM-18 File Identity Independence | `TestM11MetadataNeverParticipatesInFileIdentity` |
| CM-19 Error Location | `TestCM19ErrorIncludesSourceFileRowAndColumn`, `TestCM19StructuredMetadataErrorLocatesSourceFileAndRow` |
| CM-20 E2E | `TestCM20StructuredMetadataE2EToStorage` |
