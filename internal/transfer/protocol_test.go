package transfer

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
	"uuid"
)

func TestCapabilityCanonicalEncodingAndHash(t *testing.T) {
	shareID := uuid.NewV7().String()
	secret := [capabilitySecretBytes]byte{}
	for index := range secret {
		secret[index] = byte(index + 1)
	}
	defer clearSecret(secret[:])
	wantHash := sha256.Sum256(secret[:])
	encoded, hash, err := encodeCapabilityBytes(shareID, &secret)
	if err != nil {
		t.Fatalf("encodeCapabilityBytes: %v", err)
	}
	defer encoded.clear()
	code := string(encoded)
	if !strings.HasPrefix(code, capabilityPrefix) || len(code) != len(capabilityPrefix)+base64.RawURLEncoding.EncodedLen(capabilityPayloadBytes) {
		t.Fatal("capability shape is not canonical")
	}
	if !bytes.Equal(hash, wantHash[:]) {
		t.Fatalf("capability hash = %x, want SHA-256(secret) %x", hash, wantHash)
	}
	parsed, err := parseCapability(code)
	if err != nil {
		t.Fatalf("parseCapability: %v", err)
	}
	if parsed.shareID != shareID || parsed.secret != secret {
		t.Fatal("parsed capability fields differ")
	}
	parsed.clear()

	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(code, capabilityPrefix))
	if err != nil {
		t.Fatal(err)
	}
	wrongVersion := bytes.Clone(payload)
	wrongVersion[6] = wrongVersion[6]&0x0f | 0x40
	standardAlphabet := bytes.Clone(payload)
	copy(standardAlphabet[len(standardAlphabet)-3:], []byte{0xfb, 0xff, 0xff})
	for name, candidate := range map[string]string{
		"prefix":        "tcs2." + strings.TrimPrefix(code, capabilityPrefix),
		"padding":       code + "=",
		"short":         code[:len(code)-1],
		"uuid_version":  capabilityPrefix + base64.RawURLEncoding.EncodeToString(wrongVersion),
		"standard_base": capabilityPrefix + base64.StdEncoding.EncodeToString(standardAlphabet),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseCapability(candidate); !errors.Is(err, ErrInvalidCapability) {
				t.Fatalf("noncanonical capability error = %v, want ErrInvalidCapability", err)
			}
		})
	}
}

func TestRequestFrameIsStrictBoundedJSONV2(t *testing.T) {
	valid := []byte(`{"version":2,"share_id":"share","capability":"cap","operation":"manifest","file_id":"","offset":0,"length":0}`)
	request, err := decodeRequestFrame(valid)
	if err != nil {
		t.Fatalf("decodeRequestFrame: %v", err)
	}
	if request.Operation != operationManifest || request.Version != protocolVersion {
		t.Fatalf("decoded request = %+v", request)
	}
	for name, body := range map[string][]byte{
		"unknown":   []byte(`{"version":2,"share_id":"share","capability":"cap","operation":"manifest","target":"host"}`),
		"version":   []byte(`{"version":1,"share_id":"share","capability":"cap","operation":"manifest"}`),
		"malformed": []byte(`{"version":2`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeRequestFrame(body); protocolCode(err) != CodeProtocolInvalid {
				t.Fatalf("decode error = %v, want %s", err, CodeProtocolInvalid)
			}
		})
	}
	if _, err := decodeRequestFrame(make([]byte, MaxRequestFrameBytes+1)); protocolCode(err) != CodeLimitExceeded {
		t.Fatalf("oversized decode error = %v, want %s", err, CodeLimitExceeded)
	}
}

func TestReadRequestRejectsOversizedAndTruncatedFramesBeforeAllocation(t *testing.T) {
	for _, test := range []struct {
		name  string
		write func(net.Conn) error
		code  ErrorCode
	}{
		{
			name: "oversized",
			write: func(conn net.Conn) error {
				var prefix [4]byte
				binary.BigEndian.PutUint32(prefix[:], MaxRequestFrameBytes+1)
				_, err := conn.Write(prefix[:])
				return err
			},
			code: CodeLimitExceeded,
		},
		{
			name: "truncated",
			write: func(conn net.Conn) error {
				var prefix [4]byte
				binary.BigEndian.PutUint32(prefix[:], 8)
				if _, err := conn.Write(prefix[:]); err != nil {
					return err
				}
				_, err := conn.Write([]byte("{}"))
				return err
			},
			code: CodeProtocolInvalid,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, server := net.Pipe()
			errCh := make(chan error, 1)
			go func() {
				defer server.Close()
				_, err := readRequest(t.Context(), server)
				errCh <- err
			}()
			if err := test.write(client); err != nil {
				t.Fatal(err)
			}
			client.Close()
			if err := <-errCh; protocolCode(err) != test.code {
				t.Fatalf("readRequest error = %v, want %s", err, test.code)
			}
		})
	}
}

func TestReadResponseRejectsUnknownOversizedAndTruncatedEnvelopes(t *testing.T) {
	for _, test := range []struct {
		name string
		wire []byte
		code ErrorCode
	}{
		{name: "unknown status", wire: rawResponseFrame(9, nil), code: CodeProtocolInvalid},
		{name: "truncated", wire: append(uint32Bytes(5), responseStatusSuccess, 'x'), code: CodeProtocolInvalid},
		{name: "oversized", wire: uint32Bytes(uint32(MaxRangeResponseBytes + 2)), code: CodeLimitExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, server := net.Pipe()
			go func() {
				defer server.Close()
				_, _ = server.Write(test.wire)
			}()
			_, err := readResponse(t.Context(), client, MaxRangeResponseBytes)
			client.Close()
			if protocolCode(err) != test.code {
				t.Fatalf("readResponse error = %v, want %s", err, test.code)
			}
		})
	}
}

func TestManifestWireValidationBindsImmutableFieldsAndLimits(t *testing.T) {
	shareID := uuid.NewV7().String()
	fileID := uuid.NewV7().String()
	emptyHash := "af1349b9f5f9a1a6a0404dea36dcc9499bcb25c9adc112b7cc9a93cae41f3262"
	wire := manifestWire{
		Version:   protocolVersion,
		ShareID:   shareID,
		BlockSize: BlockSize,
		Files: []manifestFileWire{{
			FileID:      fileID,
			VirtualPath: "folder/file.txt",
			Size:        0,
			MTime:       time.Unix(1, 2).UTC().Format(time.RFC3339Nano),
			BLAKE3:      emptyHash,
			BlockSize:   BlockSize,
			BlockHashes: []string{},
		}},
	}
	manifest, err := validateManifestWire(wire, shareID)
	if err != nil {
		t.Fatalf("validateManifestWire: %v", err)
	}
	files := manifest.Files()
	if len(files) != 1 || files[0].FileID() != fileID || files[0].VirtualPath() != "folder/file.txt" || files[0].MTime().Location() != time.UTC {
		t.Fatalf("manifest files = %+v", files)
	}

	invalid := wire
	invalid.Files = append(bytesCloneManifestFiles(wire.Files), wire.Files[0])
	if errCode(validateManifestError(invalid, shareID)) != CodeProtocolInvalid {
		t.Fatal("duplicate manifest identity was accepted")
	}
	invalid = wire
	invalid.Files = bytesCloneManifestFiles(wire.Files)
	invalid.Files[0].VirtualPath = "../escape"
	if errCode(validateManifestError(invalid, shareID)) != CodeProtocolInvalid {
		t.Fatal("unsafe virtual path was accepted")
	}
	invalid = wire
	invalid.Files = bytesCloneManifestFiles(wire.Files)
	invalid.Files[0].Size = BlockSize + 1
	invalid.Files[0].BlockHashes = []string{emptyHash}
	if errCode(validateManifestError(invalid, shareID)) != CodeProtocolInvalid {
		t.Fatal("wrong block count was accepted")
	}
}

func TestRequestMarshalAndCapabilityParseBuffersAreCleared(t *testing.T) {
	shareID := uuid.NewV7().String()
	code, _, err := newTestCapability(shareID)
	if err != nil {
		t.Fatal(err)
	}
	connection := new(retainingConn)
	request := wireRequest{Version: protocolVersion, ShareID: shareID, Capability: capabilityText(code), Operation: operationManifest}
	defer request.clear()
	if err := writeRequest(t.Context(), connection, request); err != nil {
		t.Fatalf("writeRequest: %v", err)
	}
	if len(connection.writes) != 2 || !allZeroBytes(connection.writes[1]) {
		t.Fatal("encoded request body retained capability plaintext")
	}
	body, err := encodeRequestBody(request)
	if err != nil {
		t.Fatal(err)
	}
	encodedBody := body
	body = make([]byte, 0, len(encodedBody)+len(`,"unknown":1`))
	body = append(body, encodedBody[:len(encodedBody)-1]...)
	clearSecret(encodedBody)
	body = append(body, `,"unknown":1}`...)
	var unmarshalReference []byte
	if _, err := decodeRequestFrameWithCapture(body, func(kind string, secret []byte) {
		if kind == "request.unmarshal" {
			unmarshalReference = secret
		}
	}); protocolCode(err) != CodeProtocolInvalid {
		t.Fatalf("unknown-member decode error = %v", err)
	}
	clearSecret(body)
	if len(unmarshalReference) == 0 || !allZeroBytes(unmarshalReference) {
		t.Fatal("failed request unmarshal retained capability plaintext")
	}
	parsed, err := parseCapability(code)
	if err != nil {
		t.Fatal(err)
	}
	secretReference := parsed.secret[:]
	parsed.clear()
	if !allZeroBytes(secretReference) {
		t.Fatal("parsed capability secret was not cleared")
	}
}

func TestWriteRequestUsesOneClearedBodyBackingAndPreflightsLimit(t *testing.T) {
	shareID := uuid.NewV7().String()
	code, _, err := newTestCapability(shareID)
	if err != nil {
		t.Fatal(err)
	}
	fileID := strings.Repeat(`"`, 512)
	request := wireRequest{
		Version: protocolVersion, ShareID: shareID, Capability: capabilityText(code),
		Operation: operationRange, FileID: fileID, Length: 1,
	}
	defer request.clear()
	expected := []byte(`{"version":2,"share_id":"` + shareID + `","capability":"` + code +
		`","operation":"range","file_id":"` + strings.Repeat(`\"`, 512) + `","offset":0,"length":1}`)
	defer clearSecret(expected)

	var originalBacking []byte
	var wroteBody, sameBacking, wireMatches bool
	connection := &retainingConn{onWrite: func(index int, data []byte) {
		if index != 1 {
			return
		}
		wroteBody = true
		sameBacking = len(originalBacking) > 0 && &originalBacking[0] == &data[0]
		wireMatches = bytes.Equal(data, expected)
	}}
	err = writeRequestWithHooks(t.Context(), connection, request, requestWriteHooks{
		afterCapabilityCopy: func(body []byte) {
			originalBacking = body
		},
	})
	if err != nil {
		t.Fatalf("writeRequestWithHooks: %v", err)
	}
	if !wroteBody || !wireMatches {
		t.Fatal("encoded request wire bytes changed")
	}
	if !sameBacking || len(originalBacking) != len(expected) {
		t.Fatal("request body changed backing storage after copying the capability")
	}
	if len(connection.writes) != 2 || !allZeroBytes(originalBacking) || !allZeroBytes(connection.writes[1]) {
		t.Fatal("request body backing storage retained capability plaintext")
	}

	oversized := request
	oversized.FileID = strings.Repeat(`"`, MaxRequestFrameBytes)
	capabilityCopied := false
	connection = new(retainingConn)
	err = writeRequestWithHooks(t.Context(), connection, oversized, requestWriteHooks{
		afterCapabilityCopy: func([]byte) {
			capabilityCopied = true
		},
	})
	if protocolCode(err) != CodeLimitExceeded {
		t.Fatalf("oversized write error = %v, want %s", err, CodeLimitExceeded)
	}
	if capabilityCopied || len(connection.writes) != 0 {
		t.Fatal("oversized request copied the capability or reached the connection")
	}
}

func validateManifestError(wire manifestWire, shareID string) error {
	_, err := validateManifestWire(wire, shareID)
	return err
}

func errCode(err error) ErrorCode { return protocolCode(err) }

func bytesCloneManifestFiles(files []manifestFileWire) []manifestFileWire {
	cloned := make([]manifestFileWire, len(files))
	for index, file := range files {
		cloned[index] = file
		cloned[index].BlockHashes = append([]string(nil), file.BlockHashes...)
	}
	return cloned
}

func rawResponseFrame(status byte, payload []byte) []byte {
	frame := uint32Bytes(uint32(len(payload) + 1))
	frame = append(frame, status)
	return append(frame, payload...)
}

func uint32Bytes(value uint32) []byte {
	var data [4]byte
	binary.BigEndian.PutUint32(data[:], value)
	return data[:]
}

type retainingConn struct {
	writes  [][]byte
	onWrite func(int, []byte)
}

func (connection *retainingConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (connection *retainingConn) Close() error                     { return nil }
func (connection *retainingConn) LocalAddr() net.Addr              { return retainedAddr("local") }
func (connection *retainingConn) RemoteAddr() net.Addr             { return retainedAddr("remote") }
func (connection *retainingConn) SetDeadline(time.Time) error      { return nil }
func (connection *retainingConn) SetReadDeadline(time.Time) error  { return nil }
func (connection *retainingConn) SetWriteDeadline(time.Time) error { return nil }
func (connection *retainingConn) Write(data []byte) (int, error) {
	if connection.onWrite != nil {
		connection.onWrite(len(connection.writes), data)
	}
	connection.writes = append(connection.writes, data)
	return len(data), nil
}

type retainedAddr string

func (address retainedAddr) Network() string { return string(address) }
func (address retainedAddr) String() string  { return string(address) }

func allZeroBytes(data []byte) bool {
	for _, value := range data {
		if value != 0 {
			return false
		}
	}
	return true
}
