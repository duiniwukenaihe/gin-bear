# Production OpenAPI Design

**Goal:** Make generated OpenAPI documents useful enough for production API review, client generation, and Swagger UI inspection.

**Scope:** This round improves the existing reflection-based OpenAPI generator. It does not add a full annotation language, external generator, or custom Swagger UI asset bundling.

## Approach

The framework already records route method, path, and handler function type. We will use that metadata to infer a practical OpenAPI 3.0 document:

- Convert Gin path parameters from `:id` to OpenAPI `{id}`.
- Inspect handler input structs.
- Generate `path` parameters from `uri` tags.
- Generate `query` parameters from `form` and `query` tags.
- Generate `requestBody` schemas from `json` tags.
- Inspect the first non-error handler return value and emit it as the `200` response schema.
- Keep schemas inline for now to avoid unstable component names.

The generator should remain best-effort. Unsupported Go types fall back to `string` or `object` rather than failing document generation.

## Testing

Tests should mount a controller with a mixed request struct and a typed response, call `ApplyAll`, generate the OpenAPI document, and verify:

- Paths use `{id}` format.
- Path and query parameters are present.
- JSON body schema is present.
- 200 response schema is present.
- Existing Swagger endpoint still serves JSON.

## Compatibility

Existing routes continue working. The OpenAPI JSON becomes richer but keeps the same `/swagger/doc.json` endpoint.
