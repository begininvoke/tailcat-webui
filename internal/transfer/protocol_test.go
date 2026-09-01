package transfer

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
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
	code, hash, err := encodeCapability(shareID, secret)
	if err != nil {
		t.Fatalf("encodeCapability: %v", err)
	}
	if !strings.HasPrefix(code, capabilityPrefix) || len(code) != len(capabilityPrefix)+base64.RawURLEncoding.EncodedLen(capabilityPayloadBytes) {
		t.Fatal("capability shape is not canonical")
	}
	wantHash := sha256.Sum256(secret[:])
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
