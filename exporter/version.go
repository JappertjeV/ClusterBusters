package main

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type minorVersion struct {
	Major, Minor int
}

func (v minorVersion) String() string { return fmt.Sprintf("%d.%d", v.Major, v.Minor) }

func (v minorVersion) Less(o minorVersion) bool {
	if v.Major != o.Major {
		return v.Major < o.Major
	}
	return v.Minor < o.Minor
}

// parseMinor leest "4.17", "4.17.3" of "v1.29" en negeert alles na de minor.
func parseMinor(s string) (minorVersion, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	parts := strings.SplitN(s, ".", 3)
	if len(parts) < 2 {
		return minorVersion{}, fmt.Errorf("geen major.minor in %q", s)
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return minorVersion{}, fmt.Errorf("major in %q: %w", s, err)
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return minorVersion{}, fmt.Errorf("minor in %q: %w", s, err)
	}
	return minorVersion{major, minor}, nil
}

// kubeToOCP zet een Kubernetes-versie om naar de OpenShift-versie die hem meelevert:
// OCP 4.N ≈ Kubernetes 1.(N+13).
func kubeToOCP(k minorVersion) minorVersion {
	return minorVersion{4, k.Minor - 13}
}

// resolveTarget: TARGET_VERSION als die gezet is, anders de minor na de huidige versie.
func (s *Scanner) resolveTarget(ctx context.Context) *minorVersion {
	if s.targetOverride != "" {
		v, err := parseMinor(s.targetOverride)
		if err != nil {
			slog.Error("TARGET_VERSION ongeldig, doelversie onbekend", "err", err)
			return nil
		}
		return &v
	}
	cv, err := s.client.Resource(gvrClusterVersion).Get(ctx, "version", metav1.GetOptions{})
	if err != nil {
		slog.Warn("clusterversion niet leesbaar, doelversie onbekend", "err", err)
		return nil
	}
	cur, _, _ := unstructured.NestedString(cv.Object, "status", "desired", "version")
	v, err := parseMinor(cur)
	if err != nil {
		slog.Warn("huidige versie onleesbaar, doelversie onbekend", "err", err)
		return nil
	}
	v.Minor++
	return &v
}
