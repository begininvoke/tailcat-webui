package httpapi

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ca-x/tailcat-webui/internal/transfer"

	"github.com/labstack/echo/v5"
)

const (
	TransferUploadRoute       = "/transfers/shares/:id/files"
	transferVirtualPathHeader = "X-Tailcat-Virtual-Path"
)

type publicConfigResponse struct {
	AuthMode  string               `json:"auth_mode"`
	UnsafeSSH bool                 `json:"unsafe_ssh"`
	Version   string               `json:"version"`
	Transfers publicTransferConfig `json:"transfers"`
}

type publicTransferConfig struct {
	MaxFileBytes         int64 `json:"max_file_bytes"`
	MaxShareBytes        int64 `json:"max_share_bytes"`
	MaxJobBytes          int64 `json:"max_job_bytes"`
	MaxOwnerBytes        int64 `json:"max_owner_bytes"`
	MaxFilesPerShare     int   `json:"max_files_per_share"`
	Workers              int   `json:"workers"`
	MaxJobsPerOwner      int   `json:"max_jobs_per_owner"`
	ExpirySeconds        int64 `json:"expiry_seconds"`
	RetentionSeconds     int64 `json:"retention_seconds"`
	UploadTimeoutSeconds int64 `json:"upload_timeout_seconds"`
}

type transferStatus string

type transferShareResponse struct {
	ID         string         `json:"id"`
	ServerID   string         `json:"server_id"`
	Status     transferStatus `json:"status"`
	TotalBytes int64          `json:"total_bytes"`
	FileCount  int            `json:"file_count"`
	ExpiresAt  time.Time      `json:"expires_at"`
	ReadyAt    *time.Time     `json:"ready_at,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

type transferFileResponse struct {
	ID          string    `json:"id"`
	VirtualPath string    `json:"virtual_path"`
	Size        int64     `json:"size"`
	MTime       time.Time `json:"mtime"`
	CreatedAt   time.Time `json:"created_at"`
}

type transferJobResponse struct {
	ID            string             `json:"id"`
	ClientID      string             `json:"client_id"`
	RemoteShareID string             `json:"remote_share_id"`
	Status        transferStatus     `json:"status"`
	TotalBytes    int64              `json:"total_bytes"`
	ReceivedBytes int64              `json:"received_bytes"`
	ExpiresAt     time.Time          `json:"expires_at"`
	ErrorCode     transfer.ErrorCode `json:"error_code,omitempty"`
	StartedAt     *time.Time         `json:"started_at,omitempty"`
	FinishedAt    *time.Time         `json:"finished_at,omitempty"`
	CreatedAt     time.Time          `json:"created_at"`
	UpdatedAt     time.Time          `json:"updated_at"`
}

type transferItemResponse struct {
	ID              string         `json:"id"`
	JobID           string         `json:"job_id"`
	VirtualPath     string         `json:"virtual_path"`
	Size            int64          `json:"size"`
	Status          transferStatus `json:"status"`
	ReceivedBytes   int64          `json:"received_bytes"`
	CompletedBlocks int            `json:"completed_blocks"`
	MTime           time.Time      `json:"mtime"`
	StartedAt       *time.Time     `json:"started_at,omitempty"`
	FinishedAt      *time.Time     `json:"finished_at,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

type createTransferShareRequest struct {
	ServerID string `json:"server_id"`
}

type createTransferShareResponse struct {
	Share      transferShareResponse `json:"share"`
	Capability string                `json:"capability"`
}

type rotateTransferShareResponse struct {
	Capability string `json:"capability"`
}

type transferShareListResponse struct {
	Items []transferShareResponse `json:"items"`
}

type transferFileListResponse struct {
	Items []transferFileResponse `json:"items"`
}

type createTransferJobRequest struct {
	ClientID   string `json:"client_id"`
	Capability string `json:"capability"`
}

type transferJobListResponse struct {
	Items []transferJobResponse `json:"items"`
}

type transferItemListResponse struct {
	Items []transferItemResponse `json:"items"`
}

func isTransferUploadRequest(request *http.Request) bool {
	if request == nil || request.Method != http.MethodPost {
		return false
	}
	parts := strings.Split(request.URL.Path, "/")
	return len(parts) == 7 && parts[0] == "" && parts[1] == "api" && parts[2] == "v1" && parts[3] == "transfers" && parts[4] == "shares" && parts[5] != "" && parts[6] == "files"
}

func (a *API) createTransferShare(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var request createTransferShareRequest
	if err := c.Bind(&request); err != nil {
		return err
	}
	if request.ServerID == "" {
		return transferValidationError("The server_id field is required")
	}
	view, err := a.transfer.CreateShare(c.Request().Context(), p.ID, transfer.CreateShareInput{
		ServerID: request.ServerID, ExpiresAt: time.Now().Add(a.cfg.EffectiveTransfer().Expiry),
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, createTransferShareResponse{Share: transferShareDTO(view), Capability: view.Capability})
}

func (a *API) listTransferShares(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	views, err := a.transfer.ListShares(c.Request().Context(), p.ID)
	if err != nil {
		return err
	}
	items := make([]transferShareResponse, len(views))
	for index, view := range views {
		items[index] = transferShareDTO(view)
	}
	return c.JSON(http.StatusOK, transferShareListResponse{Items: items})
}

func (a *API) getTransferShare(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	view, err := a.transfer.Share(c.Request().Context(), p.ID, c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, transferShareDTO(view))
}

func (a *API) listTransferShareFiles(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	views, err := a.transfer.ListShareFiles(c.Request().Context(), p.ID, c.Param("id"))
	if err != nil {
		return err
	}
	items := make([]transferFileResponse, len(views))
	for index, view := range views {
		items[index] = transferFileDTO(view)
	}
	return c.JSON(http.StatusOK, transferFileListResponse{Items: items})
}

func (a *API) uploadTransferShareFile(c *echo.Context) error {
	body := c.Request().Body
	owned := true
	defer func() {
		if owned && body != nil {
			_ = body.Close()
		}
	}()
	p, err := principal(c)
	if err != nil {
		return err
	}
	share, err := a.transfer.Share(c.Request().Context(), p.ID, c.Param("id"))
	if err != nil {
		return err
	}
	if share.Status != "staging" || !share.ExpiresAt.After(time.Now()) {
		return transfer.ErrInvalidState
	}
	mediaType, _, err := mime.ParseMediaType(c.Request().Header.Get("Content-Type"))
	if err != nil || mediaType != "application/octet-stream" {
		return &APIError{Status: http.StatusUnsupportedMediaType, Code: "TRANSFER_UPLOAD_CONTENT_TYPE", Message: "Transfer uploads require application/octet-stream"}
	}
	length := c.Request().ContentLength
	if length < 0 {
		return transferValidationError("A non-negative Content-Length is required")
	}
	limits := a.cfg.EffectiveTransfer()
	if length > limits.MaxFileBytes {
		return &APIError{Status: http.StatusRequestEntityTooLarge, Code: "TRANSFER_FILE_TOO_LARGE", Message: "The transfer file exceeds the configured size limit"}
	}
	virtualPath := c.Request().Header.Get(transferVirtualPathHeader)
	if len(virtualPath) > 1024 || !utf8.ValidString(virtualPath) || transfer.ValidateVirtualPath(virtualPath) != nil {
		return transferValidationError("The transfer virtual path is invalid")
	}
	controller := http.NewResponseController(c.Response())
	if err := controller.SetReadDeadline(time.Now().Add(limits.UploadTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return fmt.Errorf("set transfer upload deadline: %w", err)
	}
	defer func() { _ = controller.SetReadDeadline(time.Time{}) }()
	bounded := http.MaxBytesReader(c.Response(), body, limits.MaxFileBytes)
	c.Request().Body = bounded
	owned = false
	view, err := a.transfer.StageFile(c.Request().Context(), p.ID, share.ID, transfer.StageFileInput{VirtualPath: virtualPath, Size: length, Body: bounded})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, transferFileDTO(view))
}

func (a *API) finalizeTransferShare(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	view, err := a.transfer.FinalizeShare(c.Request().Context(), p.ID, c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, transferShareDTO(view))
}

func (a *API) rotateTransferShare(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	capability, err := a.transfer.RotateShare(c.Request().Context(), p.ID, c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, rotateTransferShareResponse{Capability: capability})
}

func (a *API) deleteTransferShare(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if err := a.transfer.DeleteShare(c.Request().Context(), p.ID, c.Param("id")); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (a *API) createTransferJob(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var request createTransferJobRequest
	if err := c.Bind(&request); err != nil {
		return err
	}
	if request.ClientID == "" || request.Capability == "" {
		return transferValidationError("The client_id and capability fields are required")
	}
	view, err := a.transfer.CreateIncomingJob(c.Request().Context(), p.ID, transfer.CreateIncomingJobInput{
		ClientID: request.ClientID, Capability: request.Capability, ExpiresAt: time.Now().Add(a.cfg.EffectiveTransfer().Expiry),
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, transferJobDTO(view))
}

func (a *API) listTransferJobs(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	views, err := a.transfer.ListJobs(c.Request().Context(), p.ID)
	if err != nil {
		return err
	}
	items := make([]transferJobResponse, len(views))
	for index, view := range views {
		items[index] = transferJobDTO(view)
	}
	return c.JSON(http.StatusOK, transferJobListResponse{Items: items})
}

func (a *API) getTransferJob(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	view, err := a.transfer.Job(c.Request().Context(), p.ID, c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, transferJobDTO(view))
}

func (a *API) startTransferJob(c *echo.Context) error {
	return a.runTransferJobAction(c, a.transfer.StartJob)
}

func (a *API) retryTransferJob(c *echo.Context) error {
	return a.runTransferJobAction(c, a.transfer.RetryJob)
}

func (a *API) runTransferJobAction(c *echo.Context, action func(context.Context, string, string) (transfer.JobView, error)) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	view, err := action(c.Request().Context(), p.ID, c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, transferJobDTO(view))
}

func (a *API) cancelTransferJob(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if err := a.transfer.CancelJob(c.Request().Context(), p.ID, c.Param("id")); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (a *API) deleteTransferJob(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	if err := a.transfer.DeleteJob(c.Request().Context(), p.ID, c.Param("id")); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (a *API) listTransferJobItems(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	views, err := a.transfer.ListJobItems(c.Request().Context(), p.ID, c.Param("id"))
	if err != nil {
		return err
	}
	items := make([]transferItemResponse, len(views))
	for index, view := range views {
		items[index] = transferItemDTO(view)
	}
	return c.JSON(http.StatusOK, transferItemListResponse{Items: items})
}

func (a *API) getTransferJobItem(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	view, err := a.transfer.JobItem(c.Request().Context(), p.ID, c.Param("id"), c.Param("item_id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, transferItemDTO(view))
}

func (a *API) downloadTransferJobItem(c *echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	opened, err := a.transfer.OpenCompletedItem(c.Request().Context(), p.ID, c.Param("id"), c.Param("item_id"))
	if err != nil {
		return err
	}
	defer opened.Handle.Close()
	info, err := opened.Handle.Stat()
	if err != nil || info.Size() != opened.Item.Size {
		return errors.New("completed transfer item size changed")
	}
	filename := safeDownloadName(path.Base(opened.Item.VirtualPath))
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": filename})
	if disposition == "" {
		disposition = `attachment; filename="download"`
	}
	response := c.Response()
	response.Header().Set("Content-Type", "application/octet-stream")
	response.Header().Set("Content-Disposition", disposition)
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.Header().Set("Cache-Control", "private, no-store")
	rangeHeaders := c.Request().Header.Values("Range")
	if len(rangeHeaders) > 1 || (len(rangeHeaders) == 1 && !validSingleDownloadRange(rangeHeaders[0], opened.Item.Size)) {
		response.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(opened.Item.Size, 10))
		response.Header().Set("Accept-Ranges", "bytes")
		return c.NoContent(http.StatusRequestedRangeNotSatisfiable)
	}
	http.ServeContent(response, c.Request(), filename, opened.Item.MTime, opened.Handle)
	return nil
}

func validSingleDownloadRange(header string, size int64) bool {
	value, ok := strings.CutPrefix(header, "bytes=")
	if !ok || value == "" || strings.Contains(value, ",") {
		return false
	}
	startText, endText, ok := strings.Cut(value, "-")
	if !ok || strings.Contains(endText, "-") {
		return false
	}
	if size <= 0 {
		return false
	}
	if startText == "" {
		suffix, ok := parseASCIIRangeInt(endText)
		return ok && suffix > 0
	}
	start, ok := parseASCIIRangeInt(startText)
	if !ok || start >= uint64(size) {
		return false
	}
	if endText == "" {
		return true
	}
	end, ok := parseASCIIRangeInt(endText)
	return ok && start <= end
}

func parseASCIIRangeInt(text string) (uint64, bool) {
	if text == "" {
		return 0, false
	}
	var value uint64
	const maxInt64 = uint64(^uint64(0) >> 1)
	for _, character := range []byte(text) {
		if character < '0' || character > '9' {
			return 0, false
		}
		digit := uint64(character - '0')
		if value > (maxInt64-digit)/10 {
			return 0, false
		}
		value = value*10 + digit
	}
	return value, true
}

func safeDownloadName(name string) string {
	name = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return '_'
		}
		return character
	}, name)
	if name == "" || name == "." || name == ".." {
		return "download"
	}
	return name
}

func transferShareDTO(view transfer.ShareView) transferShareResponse {
	return transferShareResponse{
		ID: view.ID, ServerID: view.ServerID, Status: transferStatus(view.Status), TotalBytes: view.TotalBytes,
		FileCount: view.FileCount, ExpiresAt: view.ExpiresAt, ReadyAt: view.ReadyAt, CreatedAt: view.CreatedAt, UpdatedAt: view.UpdatedAt,
	}
}

func transferFileDTO(view transfer.FileView) transferFileResponse {
	return transferFileResponse{ID: view.ID, VirtualPath: view.VirtualPath, Size: view.Size, MTime: view.MTime, CreatedAt: view.CreatedAt}
}

func transferJobDTO(view transfer.JobView) transferJobResponse {
	return transferJobResponse{
		ID: view.ID, ClientID: view.ClientID, RemoteShareID: view.RemoteShareID, Status: transferStatus(view.Status),
		TotalBytes: view.TotalBytes, ReceivedBytes: view.ReceivedBytes, ExpiresAt: view.ExpiresAt, ErrorCode: view.ErrorCode,
		StartedAt: view.StartedAt, FinishedAt: view.FinishedAt, CreatedAt: view.CreatedAt, UpdatedAt: view.UpdatedAt,
	}
}

func transferItemDTO(view transfer.ItemView) transferItemResponse {
	return transferItemResponse{
		ID: view.ID, JobID: view.JobID, VirtualPath: view.VirtualPath, Size: view.Size, Status: transferStatus(view.Status),
		ReceivedBytes: view.ReceivedBytes, CompletedBlocks: view.CompletedBlocks, MTime: view.MTime,
		StartedAt: view.StartedAt, FinishedAt: view.FinishedAt, CreatedAt: view.CreatedAt, UpdatedAt: view.UpdatedAt,
	}
}

func transferValidationError(message string) error {
	return &APIError{Status: http.StatusUnprocessableEntity, Code: "TRANSFER_VALIDATION_ERROR", Message: message}
}
