package tailnet

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"reflect"
	"testing"

	"github.com/ca-x/tailcat-webui/ent"
	"github.com/ca-x/tailcat-webui/ent/enttest"
	"github.com/ca-x/tailcat-webui/ent/tailserver"
	"github.com/ca-x/tailcat-webui/internal/secrets"

	_ "github.com/lib-x/entsqlite"
	"tailscale.com/types/key"
)

var errFakeRuntime = errors.New("fake runtime failure")

type fakeRuntimeFactory struct {
	server      ServerRuntime
	serverErr   error
	serverSpecs []ServerSpec
	client      ClientRuntime
	clientErr   error
	clientSpecs []ClientSpec
}

func (f *fakeRuntimeFactory) NewServer(_ context.Context, spec ServerSpec) (ServerRuntime, error) {
	f.serverSpecs = append(f.serverSpecs, spec)
	return f.server, f.serverErr
}

func (f *fakeRuntimeFactory) NewClient(_ context.Context, spec ClientSpec) (ClientRuntime, error) {
	f.clientSpecs = append(f.clientSpecs, spec)
	return f.client, f.clientErr
}

type fakeServerRuntime struct {
	startErr error
	token    string
	public   string
	events   []string
	onDrain  func()
	onClose  func()
}

func (r *fakeServerRuntime) Start() error {
	r.events = append(r.events, "start")
	return r.startErr
}

func (r *fakeServerRuntime) Close() error {
	r.events = append(r.events, "close")
	if r.onClose != nil {
		r.onClose()
	}
	return nil
}

func (r *fakeServerRuntime) DrainTCP(context.Context) error {
	r.events = append(r.events, "drain")
	if r.onDrain != nil {
		r.onDrain()
	}
	return nil
}

func (r *fakeServerRuntime) ConnectionToken() string { return r.token }
func (r *fakeServerRuntime) PublicKey() string       { return r.public }
func (r *fakeServerRuntime) AddAllowedClient(key.NodePublic) {
}

type fakeClientRuntime struct {
	closed bool
}

func (r *fakeClientRuntime) Close() error {
	r.closed = true
	return nil
}

func (r *fakeClientRuntime) PublicKey() string { return "nodekey:fake-client" }

func (r *fakeClientRuntime) DiscoPing(context.Context) (PingResult, error) {
	return PingResult{}, errFakeRuntime
}

func (r *fakeClientRuntime) DialTCPPort(context.Context, uint16) (net.Conn, error) {
	return nil, errFakeRuntime
}

func (r *fakeClientRuntime) Dial(context.Context, string, string) (net.Conn, error) {
	return nil, errFakeRuntime
}

func TestManagerStartFailureUsesRuntimeFactory(t *testing.T) {
	manager, db, ownerID := newRuntimeFactoryTestManager(t, &fakeRuntimeFactory{})
	runtime := &fakeServerRuntime{startErr: errFakeRuntime}
	factory := &fakeRuntimeFactory{server: runtime}
	manager.runtimeFactory = factory
	server := db.TailServer.Create().SetUserID(ownerID).SetName("server").SetRegion("tailcat.dev").SaveX(t.Context())

	if _, err := manager.StartServer(t.Context(), ownerID, server.ID); !errors.Is(err, errFakeRuntime) {
		t.Fatalf("StartServer error = %v, want %v", err, errFakeRuntime)
	}
	if got, want := runtime.events, []string{"start", "close"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runtime events = %v, want %v", got, want)
	}
	if len(factory.serverSpecs) != 1 {
		t.Fatalf("server factory calls = %d, want 1", len(factory.serverSpecs))
	}
	if factory.serverSpecs[0].ReservedTCPHandlers == nil {
		t.Fatal("reserved TCP handler map is nil")
	}
	if manager.isServerRunning(server.ID) {
		t.Fatal("failed runtime remained registered")
	}
}

func TestManagerRestoreUsesRuntimeFactory(t *testing.T) {
	runtime := &fakeServerRuntime{token: "fake-token", public: "nodekey:fake-server"}
	factory := &fakeRuntimeFactory{server: runtime}
	manager, db, ownerID := newRuntimeFactoryTestManager(t, factory)
	server := db.TailServer.Create().SetUserID(ownerID).SetName("restore").SetRegion("tailcat.dev").SetDesiredRunning(true).SaveX(t.Context())

	if err := manager.Restore(t.Context()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	views, err := manager.ListServers(t.Context(), ownerID)
	if err != nil {
		t.Fatalf("ListServers: %v", err)
	}
	if len(views) != 1 || views[0].ID != server.ID || views[0].RuntimeState != RuntimePhaseRunning || views[0].ConnectionToken != "fake-token" || views[0].PublicKey != "nodekey:fake-server" {
		t.Fatalf("restored server view = %+v", views)
	}
	if got, want := runtime.events, []string{"start"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runtime events = %v, want %v", got, want)
	}
}

func TestManagerStopDeleteOrderingUsesRuntimeFactory(t *testing.T) {
	runtime := &fakeServerRuntime{token: "fake-token", public: "nodekey:fake-server"}
	factory := &fakeRuntimeFactory{server: runtime}
	manager, db, ownerID := newRuntimeFactoryTestManager(t, factory)
	server := db.TailServer.Create().SetUserID(ownerID).SetName("delete").SetRegion("tailcat.dev").SaveX(t.Context())
	runtime.onDrain = func() {
		row := db.TailServer.GetX(t.Context(), server.ID)
		if row.DesiredRunning {
			t.Error("desired running was not cleared before drain")
		}
	}
	runtime.onClose = func() {
		if !db.TailServer.Query().Where(tailserver.IDEQ(server.ID)).ExistX(t.Context()) {
			t.Error("server row was deleted before runtime close")
		}
	}

	if _, err := manager.StartServer(t.Context(), ownerID, server.ID); err != nil {
		t.Fatalf("StartServer: %v", err)
	}
	if err := manager.DeleteServer(t.Context(), ownerID, server.ID); err != nil {
		t.Fatalf("DeleteServer: %v", err)
	}
	if got, want := runtime.events, []string{"start", "drain", "close"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runtime events = %v, want %v", got, want)
	}
	if db.TailServer.Query().Where(tailserver.IDEQ(server.ID)).ExistX(t.Context()) {
		t.Fatal("server row still exists after delete")
	}
}

func TestManagerClientCloseUsesRuntimeFactory(t *testing.T) {
	clientRuntime := new(fakeClientRuntime)
	factory := &fakeRuntimeFactory{client: clientRuntime}
	manager, db, ownerID := newRuntimeFactoryTestManager(t, factory)
	const token = "tcoWFwWCAAAQIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAHw"
	ciphertext, err := manager.box.Seal([]byte(token), secretAD(ownerID, "client-1")+"/token")
	if err != nil {
		t.Fatal(err)
	}
	client := db.TailClient.Create().SetID("client-1").SetUserID(ownerID).SetName("client").SetServerTokenCipher(ciphertext).SetTokenHint("tco…").SaveX(t.Context())

	if _, err := manager.DialPort(t.Context(), ownerID, client.ID, 41640); !errors.Is(err, errFakeRuntime) {
		t.Fatalf("DialPort error = %v, want %v", err, errFakeRuntime)
	}
	if len(factory.clientSpecs) != 1 || factory.clientSpecs[0].ConnectionToken != token {
		t.Fatalf("client specs = %+v", factory.clientSpecs)
	}
	if err := manager.DeleteClient(t.Context(), ownerID, client.ID); err != nil {
		t.Fatalf("DeleteClient: %v", err)
	}
	if !clientRuntime.closed {
		t.Fatal("client runtime was not closed")
	}
}

func newRuntimeFactoryTestManager(t *testing.T, factory RuntimeFactory) (*Manager, *ent.Client, string) {
	t.Helper()
	db := enttest.Open(t, "sqlite3", "file:"+t.Name()+"?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	owner := db.User.Create().SetIssuer("test").SetSubject(t.Name()).SaveX(t.Context())
	box, err := secrets.NewBox(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(db, box, NewTargetPolicy(nil), NewTargetPolicy(nil), nil, false, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), factory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager, db, owner.ID
}
