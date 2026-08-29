# Spec: identity-access

## Objective

Authenticate people with one deployment-configured OpenID Connect provider and
isolate every private resource by the stable `(issuer, subject)` identity.

## Tech Stack

`go-oidc`, OAuth 2 authorization-code flow with PKCE, Ent users/sessions/login
flows, opaque server-side session cookies and Echo middleware.

## Commands

```sh
go test -race ./internal/auth ./internal/httpapi
```

## Project Structure

```text
internal/auth/       OIDC, login flow and session service
internal/httpapi/    auth endpoints and identity middleware
ent/schema/          User, Identity, Session, LoginFlow, AuditEvent
```

## Code Style

```go
type SessionReader interface {
	Resolve(context.Context, string) (Principal, error)
}
```

Interfaces are owned by the middleware that consumes them. Authorization uses
owner-scoped queries rather than fetch-then-compare checks.

## Testing Strategy

Test state, nonce and PKCE validation; expired/revoked sessions; session token
hashing; cross-user access; logout; and OIDC claim normalization. Demo auth is
available only under an explicit development flag.

## Boundaries

- Always: HTTP-only SameSite=Lax cookie, Secure outside loopback development,
  state+nonce+PKCE, session rotation, generic public errors and auth rate limits.
- Ask first: add password login, additional identity providers or admin roles.
- Never: store bearer/session tokens in browser storage, trust email as identity,
  log tokens or accept an unverified ID token.

## Success Criteria

- First valid OIDC callback provisions a user; later callbacks reuse it.
- Protected endpoints reject anonymous and cross-owner access.
- Logout revokes the database session before clearing the cookie.
- The console can retrieve the current public profile without sensitive fields.
