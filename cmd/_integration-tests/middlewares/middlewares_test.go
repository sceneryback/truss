package test

import (
	"testing"

	"golang.org/x/net/context"

	pb "github.com/TuneLab/go-truss/cmd/_integration-tests/middlewares/middlewarestest-service"
)

func TestAlwaysWrapped(t *testing.T) {
	ctx := context.Background()

	resp, err := middlewareEndpoints.AlwaysWrapped(ctx, &pb.Empty{})
	if err != nil {
		t.Fatal(err)
	}

	if !resp.Always {
		t.Error("Always middleware did not wrap AlwaysWrap endpoint")
	}

	if !resp.NotSometimes {
		t.Error("NotSometimes middleware did not wrap AlwaysWrap endpoint")
	}
}

func TestSometimesWrapped(t *testing.T) {
	ctx := context.Background()

	resp, err := middlewareEndpoints.SometimesWrapped(ctx, &pb.Empty{})
	if err != nil {
		t.Fatal(err)
	}

	if !resp.Always {
		t.Error("Always middleware did not wrap SometimesWrapped endpoint")
	}

	if resp.NotSometimes {
		t.Error("NotSometimes middleware did wrap SomtimesWrapped endpoint")
	}
}
