package httpapi

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"

	"github.com/labstack/echo/v5"
)

type jsonV2Serializer struct{}

func (jsonV2Serializer) Serialize(c *echo.Context, target any, indent string) error {
	if indent != "" {
		return json.MarshalWrite(c.Response(), target, jsontext.WithIndent(indent))
	}
	return json.MarshalWrite(c.Response(), target)
}

func (jsonV2Serializer) Deserialize(c *echo.Context, target any) error {
	if err := json.UnmarshalRead(c.Request().Body, target, json.RejectUnknownMembers(true)); err != nil {
		return echo.ErrBadRequest.Wrap(err)
	}
	return nil
}
