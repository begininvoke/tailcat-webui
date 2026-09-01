package httpapi

import (
	"errors"
	"net/http"

	"github.com/ca-x/tailcat-webui/internal/auth"
	"github.com/ca-x/tailcat-webui/internal/diagnostics"
	"github.com/ca-x/tailcat-webui/internal/secrets"
	"github.com/ca-x/tailcat-webui/internal/tailnet"
	"github.com/ca-x/tailcat-webui/internal/transfer"

	"github.com/labstack/echo/v5"
)

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code      string            `json:"code"`
	Message   string            `json:"message"`
	Fields    map[string]string `json:"fields,omitempty"`
	RequestID string            `json:"request_id,omitempty"`
}

type APIError struct {
	Status  int
	Code    string
	Message string
	Fields  map[string]string
	Err     error
}

func (e *APIError) Error() string { return e.Message }
func (e *APIError) Unwrap() error { return e.Err }

func badRequest(code, message string) error {
	return &APIError{Status: http.StatusUnprocessableEntity, Code: code, Message: message}
}

func errorHandler(c *echo.Context, err error) {
	if response, unwrapErr := echo.UnwrapResponse(c.Response()); unwrapErr == nil && response.Committed {
		return
	}
	status := http.StatusInternalServerError
	code := "INTERNAL_ERROR"
	message := "The request could not be completed"
	fields := map[string]string(nil)
	if apiErr, ok := errors.AsType[*APIError](err); ok {
		status, code, message, fields = apiErr.Status, apiErr.Code, apiErr.Message, apiErr.Fields
	} else if errors.Is(err, auth.ErrUnauthorized) {
		status, code, message = http.StatusUnauthorized, "UNAUTHORIZED", "Authentication is required"
	} else if _, ok := errors.AsType[*http.MaxBytesError](err); ok || errors.Is(err, transfer.ErrFileTooLarge) {
		status, code, message = http.StatusRequestEntityTooLarge, "TRANSFER_FILE_TOO_LARGE", "The transfer file exceeds the configured size limit"
	} else if errors.Is(err, transfer.ErrNotFound) {
		status, code, message = http.StatusNotFound, "TRANSFER_NOT_FOUND", "The transfer resource was not found"
	} else if errors.Is(err, transfer.ErrQuotaExceeded) {
		status, code, message = http.StatusConflict, "TRANSFER_QUOTA_EXCEEDED", "The transfer storage quota has been reached"
	} else if errors.Is(err, transfer.ErrSizeMismatch) {
		status, code, message = http.StatusUnprocessableEntity, "TRANSFER_SIZE_MISMATCH", "The request body does not match Content-Length"
	} else if errors.Is(err, transfer.ErrInvalidPath) {
		status, code, message = http.StatusUnprocessableEntity, "TRANSFER_VALIDATION_ERROR", "The transfer request fields are invalid"
	} else if errors.Is(err, transfer.ErrOwnerCapacity) {
		status, code, message = http.StatusTooManyRequests, "TRANSFER_OWNER_LIMIT", "This workspace has reached its active transfer-job limit"
	} else if errors.Is(err, transfer.ErrAlreadyActive) || errors.Is(err, transfer.ErrInvalidState) {
		status, code, message = http.StatusConflict, "TRANSFER_INVALID_STATE", "The transfer is not eligible for this action"
	} else if errors.Is(err, transfer.ErrServiceClosed) || errors.Is(err, transfer.ErrClosed) {
		status, code, message = http.StatusServiceUnavailable, "TRANSFERS_UNAVAILABLE", "Transfers are unavailable"
	} else if protocolErr, ok := errors.AsType[*transfer.ProtocolError](err); ok {
		switch protocolErr.Code {
		case transfer.CodeLimitExceeded:
			status, code, message = http.StatusRequestEntityTooLarge, "TRANSFER_LIMIT_EXCEEDED", "The transfer exceeds a configured limit"
		case transfer.CodeInvalidCapability, transfer.CodeProtocolInvalid:
			status, code, message = http.StatusUnprocessableEntity, string(protocolErr.Code), "The transfer capability or remote manifest is invalid"
		case transfer.CodeShareNotFound:
			status, code, message = http.StatusNotFound, "TRANSFER_NOT_FOUND", "The transfer resource was not found"
		default:
			status, code, message = http.StatusConflict, string(protocolErr.Code), "The transfer could not be completed in its current state"
		}
	} else if errors.Is(err, diagnostics.ErrNotFound) {
		status, code, message = http.StatusNotFound, "DIAGNOSTIC_NOT_FOUND", "The diagnostic resource was not found"
	} else if errors.Is(err, diagnostics.ErrInvalidKind) {
		status, code, message = http.StatusUnprocessableEntity, "DIAGNOSTIC_INVALID_KIND", "The diagnostic kind is invalid"
	} else if errors.Is(err, diagnostics.ErrInvalidLimits) {
		status, code, message = http.StatusUnprocessableEntity, "DIAGNOSTIC_INVALID_LIMITS", "The diagnostic limits are invalid"
	} else if errors.Is(err, diagnostics.ErrClientActive) {
		status, code, message = http.StatusConflict, "DIAGNOSTIC_CLIENT_ACTIVE", "This client already has an active diagnostic"
	} else if errors.Is(err, diagnostics.ErrOwnerCapacity) {
		status, code, message = http.StatusTooManyRequests, "DIAGNOSTIC_OWNER_LIMIT", "This workspace already has two active diagnostics"
	} else if errors.Is(err, diagnostics.ErrTerminal) {
		status, code, message = http.StatusConflict, "DIAGNOSTIC_NOT_RUNNING", "The diagnostic run is no longer active"
	} else if errors.Is(err, diagnostics.ErrClosed) {
		status, code, message = http.StatusServiceUnavailable, "DIAGNOSTICS_UNAVAILABLE", "Diagnostics are unavailable"
	} else if errors.Is(err, tailnet.ErrNotFound) {
		status, code, message = http.StatusNotFound, "NOT_FOUND", "The requested resource was not found"
	} else if errors.Is(err, tailnet.ErrAlreadyRunning) {
		status, code, message = http.StatusConflict, "ALREADY_RUNNING", "The server is already running"
	} else if errors.Is(err, tailnet.ErrNotRunning) {
		status, code, message = http.StatusConflict, "NOT_RUNNING", "The server is not running"
	} else if errors.Is(err, tailnet.ErrConflict) {
		status, code, message = http.StatusConflict, "CONFLICT", "A resource with the same name, path, port, or key already exists"
	} else if errors.Is(err, tailnet.ErrRestartRequired) {
		status, code, message = http.StatusConflict, "SERVER_MUST_STOP", "Stop the server before changing its mappings"
	} else if errors.Is(err, tailnet.ErrInvalid) {
		status, code, message = http.StatusUnprocessableEntity, "VALIDATION_ERROR", "The request fields are invalid"
	} else if errors.Is(err, tailnet.ErrCapacity) {
		status, code, message = http.StatusTooManyRequests, "CAPACITY_REACHED", "This workspace has reached its resource limit"
	} else if errors.Is(err, tailnet.ErrTargetDenied) {
		status, code, message = http.StatusForbidden, "TARGET_DENIED", "The target is denied by deployment policy"
	} else if errors.Is(err, secrets.ErrUnavailable) {
		status, code, message = http.StatusPreconditionFailed, "MASTER_KEY_REQUIRED", "Saved identities require a configured master key"
	} else if errors.Is(err, echo.ErrNotFound) {
		status, code, message = http.StatusNotFound, "NOT_FOUND", "The requested endpoint was not found"
	} else if errors.Is(err, echo.ErrMethodNotAllowed) {
		status, code, message = http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "The method is not allowed"
	} else if httpErr, ok := errors.AsType[*echo.HTTPError](err); ok {
		status = httpErr.Code
		if status == http.StatusBadRequest {
			code, message = "BAD_REQUEST", "The request body is invalid"
		} else if status == http.StatusNotFound {
			code, message = "NOT_FOUND", "The requested endpoint was not found"
		} else if status == http.StatusMethodNotAllowed {
			code, message = "METHOD_NOT_ALLOWED", "The method is not allowed"
		} else if status == http.StatusTooManyRequests {
			code, message = "RATE_LIMITED", "Too many requests; try again later"
		} else if status < 500 {
			code, message = "HTTP_ERROR", http.StatusText(status)
		}
	}
	requestID := c.Response().Header().Get(echo.HeaderXRequestID)
	if status >= 500 {
		c.Logger().ErrorContext(c.Request().Context(), "Request failed", "request_id", requestID, "error", err)
	}
	_ = c.JSON(status, errorEnvelope{Error: errorBody{Code: code, Message: message, Fields: fields, RequestID: requestID}})
}
