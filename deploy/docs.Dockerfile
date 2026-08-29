# syntax=docker/dockerfile:1.7
# qatlas-docs — the two sphinx sites as a pullable artefact.
#
# The image is CONTENT-ONLY (FROM scratch): nothing in it is ever run.
# deploy/update-docs.sh `docker create`s it, `docker cp`s /doc and
# /devdoc out into the qatlasd docs override directory (~/.qatlas/docs)
# and discards the container — qatlasd picks the new files up without a
# restart (see internal/routes/docs.go). Built by .github/workflows/
# docs.yml on every docsite change on main; the context is the workflow's
# docs-dist/ staging directory.
FROM scratch
COPY doc/ /doc/
COPY devdoc/ /devdoc/
COPY VERSION /VERSION
