package grpcclient_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	catalogv1 "github.com/avito-kitchen/platform/gen/grpc/catalog/v1"
	inventoryv1 "github.com/avito-kitchen/platform/gen/grpc/inventory/v1"
	merchantv1 "github.com/avito-kitchen/platform/gen/grpc/merchant/v1"
	"github.com/avito-kitchen/platform/internal/order/adapters/grpcclient"
	"github.com/avito-kitchen/platform/internal/order/app"
	"github.com/avito-kitchen/platform/internal/order/domain"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// The servers below stand in for the three dependencies. They are stubs, but
// the transport is real: the request and the response, including the details
// attached to an error status, travel through actual gRPC encoding. That is
// what these tests are for - a mock returning a Go value would prove nothing
// about the wire contract.

type merchantServer struct {
	merchantv1.UnimplementedMerchantServiceServer
	authResp *merchantv1.AuthenticateApiKeyResponse
	authErr  error
	merchant *merchantv1.Merchant
	getErr   error
}

func (s *merchantServer) AuthenticateApiKey(context.Context,
	*merchantv1.AuthenticateApiKeyRequest) (*merchantv1.AuthenticateApiKeyResponse, error) {
	if s.authErr != nil {
		return nil, s.authErr
	}
	return s.authResp, nil
}

func (s *merchantServer) GetMerchant(context.Context,
	*merchantv1.GetMerchantRequest) (*merchantv1.GetMerchantResponse, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return &merchantv1.GetMerchantResponse{Merchant: s.merchant}, nil
}

type catalogServer struct {
	catalogv1.UnimplementedCatalogServiceServer
	resp *catalogv1.QuoteItemsResponse
	err  error
}

func (s *catalogServer) QuoteItems(context.Context,
	*catalogv1.QuoteItemsRequest) (*catalogv1.QuoteItemsResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.resp, nil
}

type inventoryServer struct {
	inventoryv1.UnimplementedInventoryServiceServer
	availability *inventoryv1.GetAvailabilityResponse
	reserveResp  *inventoryv1.ReserveResponse
	reserveErr   error
	commitErr    error
	releaseErr   error
	lastReserve  *inventoryv1.ReserveRequest
	lastRelease  *inventoryv1.ReleaseReservationRequest
}

func (s *inventoryServer) GetAvailability(context.Context,
	*inventoryv1.GetAvailabilityRequest) (*inventoryv1.GetAvailabilityResponse, error) {
	return s.availability, nil
}

func (s *inventoryServer) Reserve(_ context.Context,
	req *inventoryv1.ReserveRequest) (*inventoryv1.ReserveResponse, error) {
	s.lastReserve = req
	if s.reserveErr != nil {
		return nil, s.reserveErr
	}
	return s.reserveResp, nil
}

func (s *inventoryServer) CommitReservation(context.Context,
	*inventoryv1.CommitReservationRequest) (*inventoryv1.CommitReservationResponse, error) {
	if s.commitErr != nil {
		return nil, s.commitErr
	}
	return &inventoryv1.CommitReservationResponse{}, nil
}

func (s *inventoryServer) ReleaseReservation(_ context.Context,
	req *inventoryv1.ReleaseReservationRequest) (*inventoryv1.ReleaseReservationResponse, error) {
	s.lastRelease = req
	if s.releaseErr != nil {
		return nil, s.releaseErr
	}
	return &inventoryv1.ReleaseReservationResponse{}, nil
}

// dial starts a gRPC server over an in-memory listener and returns a client
// connection to it.
func dial(t *testing.T, register func(*grpc.Server)) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	register(srv)
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		srv.Stop()
		_ = lis.Close()
	})
	return conn
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

var (
	merchantID = uuid.MustParse("11111111-1111-4111-8111-111111111111")
	itemA      = uuid.MustParse("a1111111-1111-4111-8111-111111111111")
)

// ------------------------------------------------------------- merchant

func TestAuthenticateMapsTheAnswer(t *testing.T) {
	t.Parallel()
	stub := &merchantServer{authResp: &merchantv1.AuthenticateApiKeyResponse{
		MerchantId: merchantID.String(),
		Status:     merchantv1.MerchantStatus_MERCHANT_STATUS_ACTIVE,
	}}
	conn := dial(t, func(s *grpc.Server) { merchantv1.RegisterMerchantServiceServer(s, stub) })
	directory := grpcclient.NewMerchantDirectory(conn, time.Second, time.Second)

	identity, err := directory.Authenticate(testCtx(t), "key")
	if err != nil {
		t.Fatalf("Authenticate() = %v", err)
	}
	if identity.MerchantID != merchantID || !identity.Active {
		t.Fatalf("identity = %+v", identity)
	}

	stub.authResp.Status = merchantv1.MerchantStatus_MERCHANT_STATUS_SUSPENDED
	identity, err = directory.Authenticate(testCtx(t), "key")
	if err != nil || identity.Active {
		t.Fatalf("a suspended outlet must be reported as inactive: %+v, %v", identity, err)
	}
}

// An unknown key and an outage must not collapse into one answer.
func TestAuthenticateSeparatesRefusalFromOutage(t *testing.T) {
	t.Parallel()
	stub := &merchantServer{authErr: status.Error(codes.NotFound, "unknown key")}
	conn := dial(t, func(s *grpc.Server) { merchantv1.RegisterMerchantServiceServer(s, stub) })
	directory := grpcclient.NewMerchantDirectory(conn, time.Second, time.Second)

	if _, err := directory.Authenticate(testCtx(t), "key"); !errors.Is(err, app.ErrMerchantUnauthenticated) {
		t.Fatalf("unknown key = %v, want ErrMerchantUnauthenticated", err)
	}

	stub.authErr = status.Error(codes.Unavailable, "connection refused")
	if _, err := directory.Authenticate(testCtx(t), "key"); !errors.Is(err, app.ErrDependencyUnavailable) {
		t.Fatalf("outage = %v, want ErrDependencyUnavailable", err)
	}
}

func TestGetMerchantMapsTheProfile(t *testing.T) {
	t.Parallel()
	stub := &merchantServer{merchant: &merchantv1.Merchant{
		Id: merchantID.String(), Name: "Кафе «Пример»",
		Status:            merchantv1.MerchantStatus_MERCHANT_STATUS_ACTIVE,
		IsAcceptingOrders: true, MinOrderMinor: 50000,
		DeliveryFeeMinor: 19900, AvgPrepMinutes: 35,
	}}
	conn := dial(t, func(s *grpc.Server) { merchantv1.RegisterMerchantServiceServer(s, stub) })
	directory := grpcclient.NewMerchantDirectory(conn, time.Second, time.Second)

	profile, err := directory.GetMerchant(testCtx(t), merchantID)
	if err != nil {
		t.Fatalf("GetMerchant() = %v", err)
	}
	if profile.Name == "" || !profile.Active || !profile.IsAcceptingOrders ||
		profile.MinOrderMinor != 50000 || profile.DeliveryFeeMinor != 19900 ||
		profile.AvgPrepMinutes != 35 {
		t.Fatalf("profile = %+v", profile)
	}

	stub.getErr = status.Error(codes.NotFound, "no such merchant")
	if _, err := directory.GetMerchant(testCtx(t), merchantID); !errors.Is(err, app.ErrMerchantNotFound) {
		t.Fatalf("GetMerchant(unknown) = %v, want ErrMerchantNotFound", err)
	}

	stub.getErr = status.Error(codes.Internal, "boom")
	if _, err := directory.GetMerchant(testCtx(t), merchantID); !errors.Is(err, app.ErrDependencyUnavailable) {
		t.Fatalf("GetMerchant(outage) = %v, want ErrDependencyUnavailable", err)
	}
}

// -------------------------------------------------------------- catalog

func TestQuoteItemsMapsProblems(t *testing.T) {
	t.Parallel()
	other := uuid.New()
	unknown := uuid.New()
	withdrawn := uuid.New()
	stub := &catalogServer{resp: &catalogv1.QuoteItemsResponse{
		MenuVersion: 7,
		Items: []*catalogv1.QuotedItem{
			{
				MenuItemId: itemA.String(), ExternalId: "POS-001", Name: "Борщ",
				UnitPriceMinor: 42000, Problem: catalogv1.ItemProblem_ITEM_PROBLEM_NONE,
			},
			{MenuItemId: unknown.String(), Problem: catalogv1.ItemProblem_ITEM_PROBLEM_NOT_FOUND},
			{MenuItemId: other.String(), Problem: catalogv1.ItemProblem_ITEM_PROBLEM_NOT_IN_MERCHANT},
			{MenuItemId: withdrawn.String(), Problem: catalogv1.ItemProblem_ITEM_PROBLEM_UNPUBLISHED},
		},
	}}
	conn := dial(t, func(s *grpc.Server) { catalogv1.RegisterCatalogServiceServer(s, stub) })
	catalogue := grpcclient.NewMenuCatalogue(conn, time.Second)

	quote, err := catalogue.QuoteItems(testCtx(t), merchantID,
		[]uuid.UUID{itemA, unknown, other, withdrawn})
	if err != nil {
		t.Fatalf("QuoteItems() = %v", err)
	}
	if quote.MenuVersion != 7 || len(quote.Items) != 4 {
		t.Fatalf("quote = %+v", quote)
	}
	want := []domain.ItemProblem{
		domain.ItemProblemNone, domain.ItemProblemNotFound,
		domain.ItemProblemNotInMerchant, domain.ItemProblemUnpublished,
	}
	for i, problem := range want {
		if quote.Items[i].Problem != problem {
			t.Fatalf("item %d problem = %s, want %s", i, quote.Items[i].Problem, problem)
		}
	}
	if quote.Items[0].UnitPriceMinor != 42000 || quote.Items[0].ExternalID != "POS-001" {
		t.Fatalf("quoted item = %+v", quote.Items[0])
	}
}

func TestQuoteItemsReportsAnOutage(t *testing.T) {
	t.Parallel()
	stub := &catalogServer{err: status.Error(codes.Unavailable, "down")}
	conn := dial(t, func(s *grpc.Server) { catalogv1.RegisterCatalogServiceServer(s, stub) })
	catalogue := grpcclient.NewMenuCatalogue(conn, time.Second)

	if _, err := catalogue.QuoteItems(testCtx(t), merchantID, []uuid.UUID{itemA}); !errors.Is(err, app.ErrDependencyUnavailable) {
		t.Fatalf("QuoteItems() = %v, want ErrDependencyUnavailable", err)
	}
}

// A request the catalogue calls malformed is our defect: retrying it cannot
// help, so it must not be reported as an outage (503).
func TestQuoteItemsReportsAMalformedRequestAsOurDefect(t *testing.T) {
	t.Parallel()
	stub := &catalogServer{err: status.Error(codes.InvalidArgument, "too many ids")}
	conn := dial(t, func(s *grpc.Server) { catalogv1.RegisterCatalogServiceServer(s, stub) })
	catalogue := grpcclient.NewMenuCatalogue(conn, time.Second)

	_, err := catalogue.QuoteItems(testCtx(t), merchantID, []uuid.UUID{itemA})
	if !errors.Is(err, app.ErrInvalidRequest) {
		t.Fatalf("QuoteItems() = %v, want ErrInvalidRequest", err)
	}
	if errors.Is(err, app.ErrDependencyUnavailable) {
		t.Fatal("a malformed request must not be reported as an outage")
	}
}

// ------------------------------------------------------------ inventory

func TestGetAvailabilityMapsTheStopList(t *testing.T) {
	t.Parallel()
	stopped := uuid.New()
	untracked := uuid.New()
	stub := &inventoryServer{availability: &inventoryv1.GetAvailabilityResponse{
		Items: []*inventoryv1.Availability{
			{
				MenuItemId: itemA.String(), Tracked: true, AvailableQty: 10,
				SalesState: inventoryv1.SalesState_SALES_STATE_ENABLED,
			},
			{
				MenuItemId: stopped.String(), Tracked: true, AvailableQty: 5,
				SalesState: inventoryv1.SalesState_SALES_STATE_STOPPED,
			},
			{MenuItemId: untracked.String()},
		},
	}}
	conn := dial(t, func(s *grpc.Server) { inventoryv1.RegisterInventoryServiceServer(s, stub) })
	ledger := grpcclient.NewStockLedger(conn, time.Second, time.Second, time.Second)

	items, err := ledger.GetAvailability(testCtx(t), merchantID,
		[]uuid.UUID{itemA, stopped, untracked})
	if err != nil {
		t.Fatalf("GetAvailability() = %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3", len(items))
	}
	if !items[0].Tracked || items[0].SalesStopped || items[0].AvailableQty != 10 {
		t.Fatalf("enabled item = %+v", items[0])
	}
	if !items[1].SalesStopped {
		t.Fatalf("stop-listed item = %+v", items[1])
	}
	if items[2].Tracked {
		t.Fatalf("untracked item = %+v", items[2])
	}
}

// The refusal detail travels in the status details, because gRPC discards the
// response body of a handler that returned an error. This test proves the
// client reads it from the wire, not from a Go value.
func TestReserveReadsTheRejectionDetail(t *testing.T) {
	t.Parallel()
	detail := &inventoryv1.ReserveResponse{
		Reserved: false,
		State:    inventoryv1.ReservationState_RESERVATION_STATE_UNSPECIFIED,
		Lines: []*inventoryv1.ReservationLineResult{
			{
				MenuItemId: itemA.String(), RequestedQty: 3, AvailableQty: 1,
				Problem: inventoryv1.LineProblem_LINE_PROBLEM_INSUFFICIENT_UNITS,
			},
		},
	}
	st, err := status.New(codes.FailedPrecondition, "cannot hold").WithDetails(detail)
	if err != nil {
		t.Fatalf("build status: %v", err)
	}
	stub := &inventoryServer{reserveErr: st.Err()}
	conn := dial(t, func(s *grpc.Server) { inventoryv1.RegisterInventoryServiceServer(s, stub) })
	ledger := grpcclient.NewStockLedger(conn, time.Second, time.Second, time.Second)

	_, err = ledger.Reserve(testCtx(t), uuid.New(), merchantID,
		[]domain.Line{{MenuItemID: itemA, Qty: 3}}, 30*time.Minute)
	rejected, ok := app.AsReserveRejected(err)
	if !ok {
		t.Fatalf("Reserve() = %v, want a rejection", err)
	}
	if len(rejected.Lines) != 1 {
		t.Fatalf("lines = %+v", rejected.Lines)
	}
	if rejected.Lines[0].Problem != domain.ItemProblemInsufficientUnits ||
		rejected.Lines[0].AvailableQty != 1 || rejected.Lines[0].RequestedQty != 3 {
		t.Fatalf("line = %+v", rejected.Lines[0])
	}
}

// A refusal without the detail is still a refusal of the basket, not an outage.
func TestReserveWithoutDetailIsStillARejection(t *testing.T) {
	t.Parallel()
	stub := &inventoryServer{reserveErr: status.Error(codes.FailedPrecondition, "cannot hold")}
	conn := dial(t, func(s *grpc.Server) { inventoryv1.RegisterInventoryServiceServer(s, stub) })
	ledger := grpcclient.NewStockLedger(conn, time.Second, time.Second, time.Second)

	_, err := ledger.Reserve(testCtx(t), uuid.New(), merchantID,
		[]domain.Line{{MenuItemID: itemA, Qty: 1}}, time.Minute)
	if _, ok := app.AsReserveRejected(err); !ok {
		t.Fatalf("Reserve() = %v, want a rejection", err)
	}
	if errors.Is(err, app.ErrDependencyUnavailable) {
		t.Fatal("a refusal must not be reported as an outage")
	}
}

// A hold refused as malformed is neither a refusal of the basket nor an
// outage: it is our defect and must not be reported as a retryable 503.
func TestReserveReportsAMalformedRequestAsOurDefect(t *testing.T) {
	t.Parallel()
	stub := &inventoryServer{reserveErr: status.Error(codes.InvalidArgument, "ttl above the maximum")}
	conn := dial(t, func(s *grpc.Server) { inventoryv1.RegisterInventoryServiceServer(s, stub) })
	ledger := grpcclient.NewStockLedger(conn, time.Second, time.Second, time.Second)

	_, err := ledger.Reserve(testCtx(t), uuid.New(), merchantID,
		[]domain.Line{{MenuItemID: itemA, Qty: 1}}, time.Minute)
	if !errors.Is(err, app.ErrInvalidRequest) {
		t.Fatalf("Reserve() = %v, want ErrInvalidRequest", err)
	}
	if errors.Is(err, app.ErrDependencyUnavailable) {
		t.Fatal("a malformed request must not be reported as an outage")
	}
	if _, ok := app.AsReserveRejected(err); ok {
		t.Fatal("a malformed request must not be reported as a refused basket")
	}
}

func TestReserveSendsTheContract(t *testing.T) {
	t.Parallel()
	reservationID := uuid.New()
	expires := time.Now().UTC().Add(30 * time.Minute).Format(time.RFC3339)
	stub := &inventoryServer{reserveResp: &inventoryv1.ReserveResponse{
		Reserved:  true,
		State:     inventoryv1.ReservationState_RESERVATION_STATE_HELD,
		ExpiresAt: expires,
	}}
	conn := dial(t, func(s *grpc.Server) { inventoryv1.RegisterInventoryServiceServer(s, stub) })
	ledger := grpcclient.NewStockLedger(conn, time.Second, time.Second, time.Second)

	result, err := ledger.Reserve(testCtx(t), reservationID, merchantID,
		[]domain.Line{{MenuItemID: itemA, Qty: 2}}, 30*time.Minute)
	if err != nil {
		t.Fatalf("Reserve() = %v", err)
	}
	if result.ExpiresAt.IsZero() {
		t.Fatal("the hold expiry must be parsed")
	}
	// The reservation identifier is the order identifier, and the TTL travels
	// in seconds.
	if stub.lastReserve.GetReservationId() != reservationID.String() {
		t.Fatalf("reservation id = %q", stub.lastReserve.GetReservationId())
	}
	if stub.lastReserve.GetTtlSeconds() != 1800 {
		t.Fatalf("ttl seconds = %d, want 1800", stub.lastReserve.GetTtlSeconds())
	}
	if len(stub.lastReserve.GetLines()) != 1 || stub.lastReserve.GetLines()[0].GetQty() != 2 {
		t.Fatalf("lines = %+v", stub.lastReserve.GetLines())
	}
}

// The dispatcher retries on an outage and gives up on a malformed message, so
// the classification of the answer is what the retry policy rests on.
func TestTerminalDecisionsAreClassified(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		err     error
		wantErr error
	}{
		{name: "success", err: nil, wantErr: nil},
		{
			name: "malformed", err: status.Error(codes.InvalidArgument, "bad id"),
			wantErr: app.ErrInvalidRequest,
		},
		{
			name: "unknown reservation", err: status.Error(codes.NotFound, "no hold"),
			wantErr: app.ErrReservationNotFound,
		},
		{
			name: "already terminal", err: status.Error(codes.FailedPrecondition, "released"),
			wantErr: app.ErrReservationNotHeld,
		},
		{
			name: "outage", err: status.Error(codes.Unavailable, "down"),
			wantErr: app.ErrDependencyUnavailable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stub := &inventoryServer{commitErr: tc.err, releaseErr: tc.err}
			conn := dial(t, func(s *grpc.Server) {
				inventoryv1.RegisterInventoryServiceServer(s, stub)
			})
			ledger := grpcclient.NewStockLedger(conn, time.Second, time.Second, time.Second)

			commitErr := ledger.Commit(testCtx(t), uuid.New())
			releaseErr := ledger.Release(testCtx(t), uuid.New(), domain.ReleaseOrderCancelled)
			for _, got := range []error{commitErr, releaseErr} {
				if tc.wantErr == nil {
					if got != nil {
						t.Fatalf("got %v, want nil", got)
					}
					continue
				}
				if !errors.Is(got, tc.wantErr) {
					t.Fatalf("got %v, want %v", got, tc.wantErr)
				}
			}
		})
	}
}

func TestReleaseSendsTheReason(t *testing.T) {
	t.Parallel()
	stub := &inventoryServer{}
	conn := dial(t, func(s *grpc.Server) { inventoryv1.RegisterInventoryServiceServer(s, stub) })
	ledger := grpcclient.NewStockLedger(conn, time.Second, time.Second, time.Second)

	cases := map[domain.ReleaseReason]inventoryv1.ReleaseReason{
		domain.ReleaseOrderRejected:  inventoryv1.ReleaseReason_RELEASE_REASON_ORDER_REJECTED,
		domain.ReleaseOrderCancelled: inventoryv1.ReleaseReason_RELEASE_REASON_ORDER_CANCELLED,
		domain.ReleaseOrderExpired:   inventoryv1.ReleaseReason_RELEASE_REASON_ORDER_EXPIRED,
	}
	for reason, want := range cases {
		if err := ledger.Release(testCtx(t), uuid.New(), reason); err != nil {
			t.Fatalf("Release(%s) = %v", reason, err)
		}
		if stub.lastRelease.GetReason() != want {
			t.Fatalf("reason = %s, want %s", stub.lastRelease.GetReason(), want)
		}
	}
	// A reason this service must never send is refused locally rather than
	// sent as UNSPECIFIED and rejected by the receiver.
	if err := ledger.Release(testCtx(t), uuid.New(), domain.ReleaseReason("NOPE")); !errors.Is(err, app.ErrInvalidRequest) {
		t.Fatalf("Release(unknown reason) = %v, want ErrInvalidRequest", err)
	}
}
