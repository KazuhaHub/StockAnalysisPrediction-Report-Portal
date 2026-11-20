package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/provision"
)

func goodArgs() []string {
	return []string{"--data-image", "/srv/p/data.img", "--data-size", "8G", "--checkpoint-image", "/srv/p/ckpt.img", "--checkpoint-size", "8G",
		"--mount-data", "/srv/p/mnt-data", "--mount-checkpoint", "/srv/p/mnt-ckpt"}
}

func neverSystem(t *testing.T) func() (provision.Env, error) {
	return func() (provision.Env, error) {
		t.Helper()
		t.Fatal("the host was reached for an invalid request")
		return provision.Env{}, nil
	}
}

func TestProvisionArgumentsAreValidatedBeforeTheHostIsTouched(t *testing.T) {
	without := func(flag string) []string {
		args := goodArgs()
		for i := 0; i < len(args); i += 2 {
			if args[i] == flag {
				return append(append([]string{}, args[:i]...), args[i+2:]...)
			}
		}
		t.Fatal(flag)
		return nil
	}
	cases := map[string][]string{
		"no arguments":       nil,
		"extra positional":   append(goodArgs(), "extra"),
		"unknown flag":       append(goodArgs(), "--force"),
		"missing data image": without("--data-image"),
		"missing data size":  without("--data-size"),
		"missing ckpt size":  without("--checkpoint-size"),
		"missing mount":      without("--mount-checkpoint"),
		"bad size":           append(goodArgs(), "--data-size", "lots"),
		"too small":          append(goodArgs(), "--checkpoint-size", "100M"),
		"relative image":     append(goodArgs(), "--data-image", "data.img"),
		"image inside mount": append(goodArgs(), "--data-image", "/srv/p/mnt-data/data.img"),
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			if code := run(context.Background(), append([]string{"provision-xfs-images"}, args...), &out); code != 2 {
				t.Fatalf("code %d: %s", code, out.String())
			}
			if code := provisionXFSImages(context.Background(), args, &out, neverSystem(t)); code != 2 {
				t.Fatalf("code %d", code)
			}
		})
	}
}

func TestProvisionHelpAndUsageNameTheRecipe(t *testing.T) {
	var out bytes.Buffer
	if code := provisionXFSImages(context.Background(), []string{"--help"}, &out, neverSystem(t)); code != 0 || !strings.Contains(out.String(), "--mount-checkpoint") || !strings.Contains(out.String(), "182-cross-platform-backend-runtime.md") {
		t.Fatalf("help: %d %s", code, out.String())
	}
	out.Reset()
	if code := run(context.Background(), nil, &out); code != 2 || !strings.Contains(out.String(), "provision-xfs-images") {
		t.Fatalf("usage: %d %s", code, out.String())
	}
}

func TestProvisionExitCodesFollowTheOutcome(t *testing.T) {
	// An unsupported platform and a host that is not root are operational failures (1), never usage errors.
	var out bytes.Buffer
	unsupported := func() (provision.Env, error) { return provision.Env{}, provision.ErrUnsupported }
	if code := provisionXFSImages(context.Background(), goodArgs(), &out, unsupported); code != 1 || !strings.Contains(out.String(), "requires Linux") {
		t.Fatalf("%d %s", code, out.String())
	}
	out.Reset()
	if code := provisionXFSImages(context.Background(), goodArgs(), &out, func() (provision.Env, error) { return provision.Env{}, errors.New("boom") }); code != 1 {
		t.Fatalf("%d", code)
	}
}
