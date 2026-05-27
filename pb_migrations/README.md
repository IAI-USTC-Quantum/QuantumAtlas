# PocketBase Migrations

Migration files applied on PocketBase startup in lexicographic order.

P2 will land `0001_github_oauth.go` here to auto-configure the GitHub OAuth
provider on the built-in `users` collection from `GITHUB_CLIENT_ID` /
`GITHUB_CLIENT_SECRET` env vars.
