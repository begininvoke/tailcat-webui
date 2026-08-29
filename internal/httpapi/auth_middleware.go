package httpapi

import (
	"errors"
	"net/http"

	"github.com/ca-x/tailcat-webui/internal/auth"
	"github.com/labstack/echo/v5"
)

const principalKey = "tailcat.principal"

func (a *API) optionalPrincipal(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		cookie, err := c.Cookie(sessionCookie)
		if err == nil {
			principal, resolveErr := a.auth.ResolveSession(c.Request().Context(), cookie.Value)
			if resolveErr == nil {
				c.Set(principalKey, principal)
			}
		}
		return next(c)
	}
}

func (a *API) requirePrincipal(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		cookie, err := c.Cookie(sessionCookie)
		if err != nil {
			return auth.ErrUnauthorized
		}
		principal, err := a.auth.ResolveSession(c.Request().Context(), cookie.Value)
		if err != nil {
			return auth.ErrUnauthorized
		}
		c.Set(principalKey, principal)
		return next(c)
	}
}

func principal(c *echo.Context) (auth.Principal, error) {
	value := c.Get(principalKey)
	principal, ok := value.(auth.Principal)
	if !ok || principal.ID == "" {
		return auth.Principal{}, auth.ErrUnauthorized
	}
	return principal, nil
}

func optionalPrincipalID(c *echo.Context) string {
	value := c.Get(principalKey)
	principal, _ := value.(auth.Principal)
	return principal.ID
}

func sessionToken(c *echo.Context) string {
	cookie, err := c.Cookie(sessionCookie)
	if err != nil && !errors.Is(err, http.ErrNoCookie) {
		return ""
	}
	if cookie == nil {
		return ""
	}
	return cookie.Value
}
