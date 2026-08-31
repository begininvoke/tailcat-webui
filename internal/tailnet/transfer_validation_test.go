package tailnet

import (
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ca-x/tailcat-webui/ent"
	"github.com/ca-x/tailcat-webui/ent/enttest"

	_ "github.com/lib-x/entsqlite"
)

const transferBlockSize = int64(8 * 1024 * 1024)

func TestTransferMetadataRejectsInvalidDirectWrites(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:transfer-validation?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	ctx := t.Context()
	owner := db.User.Create().SetIssuer("test").SetSubject("validation-owner").SaveX(ctx)
	other := db.User.Create().SetIssuer("test").SetSubject("validation-other").SaveX(ctx)
	server := db.TailServer.Create().SetUserID(owner.ID).SetName("validation-server").SaveX(ctx)
	otherServer := db.TailServer.Create().SetUserID(other.ID).SetName("validation-other-server").SaveX(ctx)
	client := db.TailClient.Create().SetUserID(owner.ID).SetName("validation-client").SetServerTokenCipher([]byte("cipher")).SetTokenHint("tc…").SaveX(ctx)
	otherClient := db.TailClient.Create().SetUserID(other.ID).SetName("validation-other-client").SetServerTokenCipher([]byte("cipher")).SetTokenHint("tc…").SaveX(ctx)
	share := newTransferShare(db, owner.ID, server.ID).SaveX(ctx)
	otherShare := newTransferShare(db, other.ID, otherServer.ID).SaveX(ctx)
	job := newTransferJob(db, owner.ID, client.ID).SaveX(ctx)
	otherJob := newTransferJob(db, other.ID, otherClient.ID).SaveX(ctx)

	for _, size := range []int{0, 31, 33} {
		t.Run(fmt.Sprintf("capability_hash_%d_bytes", size), func(t *testing.T) {
			if _, err := newTransferShare(db, owner.ID, server.ID).SetCapabilityHash(make([]byte, size)).Save(ctx); err == nil {
				t.Fatalf("capability hash length %d was accepted", size)
			}
		})
	}

	for _, remoteID := range []string{"not-a-uuid", "01900000000070008000000000000001", "01900000-0000-4000-8000-000000000001", "01900000-0000-7000-8000-00000000000A"} {
		t.Run("remote_id_"+remoteID, func(t *testing.T) {
			if _, err := newTransferJob(db, owner.ID, client.ID).SetRemoteShareID(remoteID).Save(ctx); err == nil {
				t.Fatalf("remote share id %q was accepted", remoteID)
			}
			if _, err := newTransferItem(db, owner.ID, job.ID, 0, nil, 0).SetRemoteFileID(remoteID).Save(ctx); err == nil {
				t.Fatalf("remote file id %q was accepted", remoteID)
			}
		})
	}

	for _, storageName := range []string{"", ".", "..", "short", "file/name", `file\name`, "C:disk", "CON", "AUX", "NUL", "COM1", "LPT9", "name\x00", string([]byte{0xff}), strings.Repeat("a", 129)} {
		storageName := storageName
		t.Run("storage_name_"+strings.ReplaceAll(storageName, "/", "_"), func(t *testing.T) {
			if _, err := newShareFile(db, owner.ID, share.ID, 0, nil).SetStorageName(storageName).Save(ctx); err == nil {
				t.Fatalf("storage name %q was accepted", storageName)
			}
		})
	}

	invalidUTF8 := string([]byte{0xff})
	for _, virtualPath := range []string{"", "/absolute", `\\host\share`, "C:/volume", "CON/report.txt", "a//b", "a/./b", "a/../b", `a\b`, "a\x00b", invalidUTF8, strings.Repeat("a", 1025), strings.Repeat("a/", 32) + "z"} {
		virtualPath := virtualPath
		t.Run("virtual_path", func(t *testing.T) {
			if _, err := newShareFile(db, owner.ID, share.ID, 0, nil).SetVirtualPath(virtualPath).Save(ctx); err == nil {
				t.Fatalf("virtual path %q was accepted", virtualPath)
			}
		})
	}
	if !utf8.ValidString("folder/report.txt") {
		t.Fatal("test fixture is not UTF-8")
	}
	if _, err := newShareFile(db, owner.ID, share.ID, 0, nil).SetVirtualPath("folder/report.txt").Save(ctx); err != nil {
		t.Fatalf("valid virtual path: %v", err)
	}
	if _, err := db.TransferShare.UpdateOneID(share.ID).SetCapabilityHash([]byte("abcdefghijklmnopqrstuvwxyzABCDEF")).Save(ctx); err != nil {
		t.Fatalf("capability hash rotation failed: %v", err)
	}

	for _, test := range []struct {
		name   string
		size   int64
		blocks []string
	}{
		{name: "zero_with_block", size: 0, blocks: []string{validBLAKE3}},
		{name: "partial_missing_block", size: 1, blocks: nil},
		{name: "exact_boundary_extra_block", size: transferBlockSize, blocks: []string{validBLAKE3, validBLAKE3}},
	} {
		t.Run("manifest_"+test.name, func(t *testing.T) {
			if _, err := newShareFile(db, owner.ID, share.ID, test.size, test.blocks).Save(ctx); err == nil {
				t.Fatal("invalid block manifest was accepted")
			}
		})
	}
	if _, err := newTransferJob(db, owner.ID, client.ID).SetTotalBytes(1).SetReceivedBytes(2).Save(ctx); err == nil {
		t.Fatal("job received bytes above total were accepted on create")
	}
	for _, test := range []struct {
		name   string
		size   int64
		blocks []string
	}{
		{name: "zero", size: 0, blocks: nil},
		{name: "exact_boundary", size: transferBlockSize, blocks: []string{validBLAKE3}},
	} {
		t.Run("manifest_"+test.name, func(t *testing.T) {
			if _, err := newShareFile(db, owner.ID, share.ID, test.size, test.blocks).Save(ctx); err != nil {
				t.Fatalf("valid block manifest: %v", err)
			}
		})
	}

	for _, test := range []struct {
		name      string
		size      int64
		completed []int
		received  int64
	}{
		{name: "negative_block", size: 1, completed: []int{-1}, received: 0},
		{name: "duplicate_block", size: 1, completed: []int{0, 0}, received: 1},
		{name: "out_of_range_block", size: 1, completed: []int{1}, received: 1},
		{name: "received_too_large", size: 1, completed: nil, received: 2},
		{name: "completed_without_bytes", size: 1, completed: []int{0}, received: 0},
	} {
		t.Run("item_"+test.name, func(t *testing.T) {
			if _, err := newTransferItem(db, owner.ID, job.ID, test.size, test.completed, test.received).Save(ctx); err == nil {
				t.Fatal("invalid resumable item was accepted")
			}
		})
	}
	for _, test := range []struct {
		name     string
		size     int64
		complete []int
		received int64
	}{
		{name: "zero", size: 0, complete: nil, received: 0},
		{name: "exact_boundary", size: transferBlockSize, complete: []int{0}, received: transferBlockSize},
	} {
		t.Run("item_valid_"+test.name, func(t *testing.T) {
			if _, err := newTransferItem(db, owner.ID, job.ID, test.size, test.complete, test.received).Save(ctx); err != nil {
				t.Fatalf("valid resumable item: %v", err)
			}
		})
	}
	item := newTransferItem(db, owner.ID, job.ID, transferBlockSize+1, []int{0}, transferBlockSize).SaveX(ctx)
	if _, err := db.TransferItem.UpdateOneID(item.ID).SetReceivedBytes(item.SizeBytes + 1).Save(ctx); err == nil {
		t.Fatal("received bytes above item size were accepted on update")
	}
	if _, err := db.TransferItem.UpdateOneID(item.ID).SetCompletedBlocks([]int{0, 1}).SetReceivedBytes(transferBlockSize).Save(ctx); err == nil {
		t.Fatal("completed final block without its bytes was accepted on update")
	}
	if _, err := db.TransferItem.UpdateOneID(item.ID).AppendCompletedBlocks([]int{1}).Save(ctx); err == nil {
		t.Fatal("appended completed block without its bytes was accepted on update")
	}
	if _, err := db.TransferItem.Update().SetReceivedBytes(item.SizeBytes + 1).Save(ctx); err == nil {
		t.Fatal("bulk item update was accepted")
	}
	if _, err := db.TransferItem.UpdateOneID(item.ID).AddReceivedBytes(1).Save(ctx); err == nil {
		t.Fatal("additive item byte update was accepted")
	}

	if _, err := db.TransferJob.UpdateOneID(job.ID).SetTotalBytes(1).SetReceivedBytes(2).Save(ctx); err == nil {
		t.Fatal("job received bytes above total were accepted on update")
	}
	if _, err := db.TransferJob.Update().SetReceivedBytes(1).Save(ctx); err == nil {
		t.Fatal("bulk job update was accepted")
	}
	if _, err := db.TransferJob.UpdateOneID(job.ID).AddReceivedBytes(1).Save(ctx); err == nil {
		t.Fatal("additive job byte update was accepted")
	}

	if _, err := newTransferShare(db, owner.ID, otherServer.ID).Save(ctx); err == nil {
		t.Fatal("cross-owner server share was accepted")
	}
	if _, err := newTransferJob(db, owner.ID, otherClient.ID).Save(ctx); err == nil {
		t.Fatal("cross-owner client job was accepted")
	}
	if _, err := newShareFile(db, owner.ID, otherShare.ID, 0, nil).Save(ctx); err == nil {
		t.Fatal("cross-owner share file was accepted")
	}
	if _, err := newTransferItem(db, owner.ID, otherJob.ID, 0, nil, 0).Save(ctx); err == nil {
		t.Fatal("cross-owner job item was accepted")
	}
}

func TestTransferMigrationUsesCompositeOwnershipForeignKeys(t *testing.T) {
	dsn := "file:transfer-composite-fks?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	_ = enttest.Open(t, "sqlite3", dsn)
	raw, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	for _, relation := range []struct {
		table    string
		columns  [2]string
		referred [2]string
	}{
		{table: "transfer_shares", columns: [2]string{"user_id", "server_id"}, referred: [2]string{"user_id", "id"}},
		{table: "share_files", columns: [2]string{"user_id", "share_id"}, referred: [2]string{"user_id", "id"}},
		{table: "transfer_jobs", columns: [2]string{"user_id", "client_id"}, referred: [2]string{"user_id", "id"}},
		{table: "transfer_items", columns: [2]string{"user_id", "job_id"}, referred: [2]string{"user_id", "id"}},
	} {
		relation := relation
		t.Run(relation.table, func(t *testing.T) {
			rows, err := raw.Query("PRAGMA foreign_key_list(" + relation.table + ")")
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			matchedColumns := make(map[int]map[int]bool)
			for rows.Next() {
				var id, seq int
				var table, from, to, onUpdate, onDelete, match string
				if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
					t.Fatal(err)
				}
				if seq < len(relation.columns) && from == relation.columns[seq] && to == relation.referred[seq] && onDelete == "CASCADE" {
					if matchedColumns[id] == nil {
						matchedColumns[id] = make(map[int]bool)
					}
					matchedColumns[id][seq] = true
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			matched := false
			for _, columns := range matchedColumns {
				if columns[0] && columns[1] {
					matched = true
					break
				}
			}
			if !matched {
				t.Fatalf("missing composite ownership FK %v -> %v", relation.columns, relation.referred)
			}
		})
	}
	for _, table := range []string{"tail_servers", "tail_clients", "transfer_shares", "transfer_jobs"} {
		table := table
		t.Run(table+"_owner_id_index", func(t *testing.T) {
			rows, err := raw.Query("PRAGMA index_list(" + table + ")")
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			found := false
			for rows.Next() {
				var sequence int
				var name string
				var unique int
				var origin string
				var partial int
				if err := rows.Scan(&sequence, &name, &unique, &origin, &partial); err != nil {
					t.Fatal(err)
				}
				if unique != 1 {
					continue
				}
				columns, err := raw.Query("PRAGMA index_info(" + name + ")")
				if err != nil {
					t.Fatal(err)
				}
				var names []string
				for columns.Next() {
					var sequence, columnID int
					var column string
					if err := columns.Scan(&sequence, &columnID, &column); err != nil {
						columns.Close()
						t.Fatal(err)
					}
					names = append(names, column)
				}
				if err := columns.Close(); err != nil {
					t.Fatal(err)
				}
				if strings.Join(names, ",") == "user_id,id" {
					found = true
					break
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if !found {
				t.Fatal("missing unique (user_id, id) index")
			}
		})
	}
}

const validBLAKE3 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

var transferValidationFileID atomic.Int64

func validCapabilityHash() []byte { return []byte("01234567890123456789012345678901") }

func newTransferShare(db *ent.Client, ownerID, serverID string) *ent.TransferShareCreate {
	return db.TransferShare.Create().SetUserID(ownerID).SetServerID(serverID).SetCapabilityHash(validCapabilityHash()).SetExpiresAt(time.Now().Add(time.Hour))
}

func newTransferJob(db *ent.Client, ownerID, clientID string) *ent.TransferJobCreate {
	return db.TransferJob.Create().SetUserID(ownerID).SetClientID(clientID).SetRemoteShareID("01900000-0000-7000-8000-000000000001").SetRemoteCapabilityCipher([]byte("encrypted-capability")).SetExpiresAt(time.Now().Add(time.Hour))
}

func newShareFile(db *ent.Client, ownerID, shareID string, size int64, blockHashes []string) *ent.ShareFileCreate {
	pathID := transferValidationFileID.Add(1)
	return db.ShareFile.Create().SetUserID(ownerID).SetShareID(shareID).SetStorageName("outgoing-random-name").SetVirtualPath(fmt.Sprintf("fixture-%d/report.txt", pathID)).SetSizeBytes(size).SetMtime(time.Now()).SetBlake3(validBLAKE3).SetBlockHashes(blockHashes)
}

func newTransferItem(db *ent.Client, ownerID, jobID string, size int64, completedBlocks []int, receivedBytes int64) *ent.TransferItemCreate {
	fileID := transferValidationFileID.Add(1)
	blockHashes := make([]string, transferBlockCountForTest(size))
	for index := range blockHashes {
		blockHashes[index] = validBLAKE3
	}
	return db.TransferItem.Create().SetUserID(ownerID).SetJobID(jobID).SetRemoteFileID(fmt.Sprintf("01900000-0000-7000-8000-%012d", fileID)).SetStorageName("incoming-random-name").SetVirtualPath("report.txt").SetSizeBytes(size).SetMtime(time.Now()).SetBlake3(validBLAKE3).SetBlockHashes(blockHashes).SetCompletedBlocks(completedBlocks).SetReceivedBytes(receivedBytes)
}

func transferBlockCountForTest(size int64) int {
	if size == 0 {
		return 0
	}
	count := int(size / transferBlockSize)
	if size%transferBlockSize != 0 {
		count++
	}
	return count
}
