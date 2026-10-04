package coordgrpc_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	pb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
)

// TestLaunch_WireCodec_FieldSetsMatch: the Go and proto field sets are the
// same names, so a field added to one without the other fails here.
func TestLaunch_WireCodec_FieldSetsMatch(t *testing.T) {
	var goFields []string
	typ := reflect.TypeOf(launch.Launch{})
	for i := 0; i < typ.NumField(); i++ {
		goFields = append(goFields, typ.Field(i).Name)
	}
	require.ElementsMatch(t, goFields, coordgrpc.WireFieldNames(), "launch.Launch and the proto Launch must carry the same field set")
}

// TestEncodeLaunch_EveryWireFieldIsPopulated_AndDecodeRoundTrips: a fully
// populated Launch sets every field of the wire message (a wire field the
// encoder never writes is a field the runner can never receive), and the
// decoder recovers the same value — the two ends are one codec.
func TestEncodeLaunch_EveryWireFieldIsPopulated_AndDecodeRoundTrips(t *testing.T) {
	l := launchtest.FullLaunch(t)
	wire := coordgrpc.EncodeLaunch(l)

	msg := wire.ProtoReflect()
	fields := msg.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		require.Truef(t, msg.Has(f), "wire field %q is not populated by EncodeLaunch from a fully populated Launch", f.Name())
	}

	back, err := coordgrpc.DecodeLaunch(wire)
	require.NoError(t, err)
	require.Equal(t, launchtest.Comparable(l), launchtest.Comparable(back), "DecodeLaunch(EncodeLaunch(l)) is l (the cell's process-local handles excepted)")

	// The wire form survives serialisation: the runner reads bytes, not a
	// Go value.
	raw, err := proto.Marshal(wire)
	require.NoError(t, err)
	var parsed pb.Launch
	require.NoError(t, proto.Unmarshal(raw, &parsed))
	again, err := coordgrpc.DecodeLaunch(&parsed)
	require.NoError(t, err)
	require.Equal(t, launchtest.Comparable(l), launchtest.Comparable(again))
}

// TestEncodeLaunch_CarrierShapeIsTheOneof: a claim rides as a claim and an
// inline package as bytes; the decoder hands each back with the digest.
func TestEncodeLaunch_CarrierShapeIsTheOneof(t *testing.T) {
	l := launchtest.FullLaunch(t)
	inline := coordgrpc.EncodeLaunch(l).GetPackage()
	require.NotNil(t, inline.GetInline(), "an inline carrier rides the frame as bytes")
	require.Nil(t, inline.GetClaim())

	claimed := l
	claimed.Package = composite.Carrier{Claim: &composite.Claim{Location: "harp/persist/package/abc", Size: 3}, Digest: l.Package.Digest}
	wire := coordgrpc.EncodeLaunch(claimed).GetPackage()
	require.NotNil(t, wire.GetClaim(), "a claim carrier rides the frame as a claim")
	require.Nil(t, wire.GetInline())
	back, err := coordgrpc.DecodeLaunch(coordgrpc.EncodeLaunch(claimed))
	require.NoError(t, err)
	require.Equal(t, claimed.Package, back.Package)
}

// TestDecodeLaunch_RefusesAnAbsentLaunch: a StartRun without its launch is
// a refusal, not a zero Launch the runner would deliver nothing from.
func TestDecodeLaunch_RefusesAnAbsentLaunch(t *testing.T) {
	_, err := coordgrpc.DecodeLaunch(nil)
	require.ErrorIs(t, err, coordgrpc.ErrNoLaunch)
}

// TestMaxRecvMsgSize_BoundsTheInlinePackagePlusHeadroom: the frame ceiling
// is the inline ceiling plus one MiB of headroom for the rest of the Launch,
// stated once and read by both ends.
func TestMaxRecvMsgSize_BoundsTheInlinePackagePlusHeadroom(t *testing.T) {
	require.Equal(t, composite.DefaultInlineMax+(1<<20), coordgrpc.MaxRecvMsgSize)
}

var _ protoreflect.Message = (*pb.Launch)(nil).ProtoReflect()

// TestCell_CarriesNoContainerHalf: nothing ever produced a container half of
// a cell (the Environment owns the runtime), so neither the Go cell nor its
// wire form may carry one — a field nobody writes is a field a reader will
// trust. The wire number and name stay reserved so they cannot be reused
// with another meaning.
func TestCell_CarriesNoContainerHalf(t *testing.T) {
	_, has := reflect.TypeOf(launch.Cell{}).FieldByName("Container")
	require.False(t, has, "launch.Cell has no container half")

	d := (&pb.Cell{}).ProtoReflect().Descriptor()
	require.Nil(t, d.Fields().ByNumber(6), "wire Cell field 6 is gone")
	require.True(t, d.ReservedRanges().Has(protoreflect.FieldNumber(6)), "wire Cell field 6 is reserved")
	require.True(t, d.ReservedNames().Has("container"), "wire Cell name \"container\" is reserved")
}

// TestDecodeLaunch_AnUnsetVerdictIsUntrusted: a launch whose originator set
// no verdict — or one this runner does not know — lets no repository surface
// load.
func TestDecodeLaunch_AnUnsetVerdictIsUntrusted(t *testing.T) {
	for _, w := range []pb.WorkspaceTrust{pb.WorkspaceTrust_WORKSPACE_TRUST_UNSPECIFIED, pb.WorkspaceTrust_WORKSPACE_TRUST_UNTRUSTED, pb.WorkspaceTrust(99)} {
		wire := coordgrpc.EncodeLaunch(launchtest.FullLaunch(t))
		wire.Trust = w
		back, err := coordgrpc.DecodeLaunch(wire)
		require.NoError(t, err)
		require.Equal(t, engine.TrustUntrusted, back.Trust, "wire %v", w)
	}
}

// TestCell_WireFieldNumbersArePinned: a field's number IS its wire identity
// — a runner built from one branch reads field 8 as whatever its own .proto
// says field 8 is. Branches adding fields in parallel each reach for the
// next free number, and renumbering one on merge is only safe while nothing
// has shipped; this pins the whole set and the reserved holes, so a
// renumbering or reuse fails here instead of misreading bytes in production.
func TestCell_WireFieldNumbersArePinned(t *testing.T) {
	want := map[protoreflect.Name]protoreflect.FieldNumber{
		"paths": 1, "env": 4, "home": 5,
		"unset_env": 7, "env_host": 8, "secret_files": 9,
	}
	desc := (&pb.Cell{}).ProtoReflect().Descriptor()
	got := map[protoreflect.Name]protoreflect.FieldNumber{}
	for i := 0; i < desc.Fields().Len(); i++ {
		f := desc.Fields().Get(i)
		got[f.Name()] = f.Number()
	}
	require.Equal(t, want, got)
	for _, n := range []protoreflect.FieldNumber{2, 3, 6} {
		require.True(t, desc.ReservedRanges().Has(n), "%d is a retired field's number (mounts, workspace, the container half); reusing it would misread old bytes", n)
	}
}

// TestCell_EnvHostAndSecretFilesCrossTheWireTogether: the two newest cell
// fields each come back as sent, from serialised bytes, in one message.
func TestCell_EnvHostAndSecretFilesCrossTheWireTogether(t *testing.T) {
	l := launchtest.FullLaunch(t)
	require.NotEmpty(t, l.Cell.SecretFiles)
	require.True(t, l.Cell.EnvHost.Curated)

	raw, err := proto.Marshal(coordgrpc.EncodeLaunch(l))
	require.NoError(t, err)
	var parsed pb.Launch
	require.NoError(t, proto.Unmarshal(raw, &parsed))
	back, err := coordgrpc.DecodeLaunch(&parsed)
	require.NoError(t, err)
	require.Equal(t, l.Cell.SecretFiles, back.Cell.SecretFiles)
	require.Equal(t, l.Cell.EnvHost, back.Cell.EnvHost)
}
