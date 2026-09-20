# Recipe: add a resource end to end

Goal: scaffold nothing new — add one CRUD resource to an existing project,
migrate, and test it.

```sh
cd <project>
bear gen api invoice --fields "name:string,email:email" --dry-run --format json
bear gen api invoice --fields "name:string,email:email"
go mod tidy
go run ./cmd/migrate          # reviewed SQL, separate deploy step
go test ./... -count=1
```

Verify: `migrations/001_create_invoice.up.sql` matches `database.type`
(postgres uses `BIGSERIAL`, mysql backticks + `AUTO_INCREMENT`); the server
still refuses to start a migration by itself — schema changes only happen via
`cmd/migrate`.

Non-goals: changing the framework, editing applied migrations, committing
secrets. Rollback: delete `internal/invoice`, the migration pair, and the
manifest entry (or re-run generation in a scratch copy and diff).
