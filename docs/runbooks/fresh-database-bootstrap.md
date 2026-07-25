# Fresh database bootstrap

> Last verified 2026-07-25. Architectural background is `blueprint.md` §5
> (fresh baseline) and §7.4 (initialization and release).

Production deployment supports a new, empty database only. It does not support
an in-place upgrade from an older connect-it schema, and it does not preserve or
transform data created by an earlier schema.

The production Server is runtime-only: it validates the expected database
identity and current schema before listening, but it never creates or migrates
the schema.

Build and publish `packages/api/cmd/connect-it-db` as a standalone database
tool, separate from the Server image. Point `DATABASE_URL` at the one intended
empty schema, then initialize it before starting Server:

```sh
DATABASE_URL='postgres://…?search_path=connect_it' connect-it-db init
```

The command succeeds only for a completely empty sole effective schema or an
already-current `v1/clean` schema. It has no upgrade, down, force, or recovery
operation. Do not substitute the development/CI bootstrap script or run the
baseline SQL by hand in production.

Local Compose, `mise run dev`, and the isolated ingress test are the only
consumers of `scripts/configure-dev-database.sql`. That file declares one fixed
fresh-v1 owner/application-role policy; it does not discover, scrub, or upgrade
legacy ACLs. `scripts/bootstrap-dev-compose-db.sh` additionally pins the exact
Compose project/files and can publish a no-clobber, mode-0600 database identity
override. Neither script is a production provisioning interface.

Postgres, Server, and Web release images must use distinct immutable digests.
Run `mise run check-release-compose` for the repository model, then run
`node scripts/check-release-compose.mjs` with the three actual
`CONNECT_IT_*_IMAGE` values before deployment. Provider real-account smoke and
Docker/Nginx ingress acceptance remain separate release requirements.
