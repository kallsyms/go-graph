package coordination

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/sirupsen/logrus"
	"golang.org/x/tools/go/packages"
)

type packageStatus int

const (
	Pending    packageStatus = 0
	InProgress               = 1
	Done                     = 2
)

func PkgVersion(pkg *packages.Package, fallback string) string {
	if pkg.Module != nil {
		return pkg.Module.Version
	} else {
		// This is to try and identify if a package is from GOROOT, and use runtime version if so.
		if (len(pkg.GoFiles) > 0 && strings.HasPrefix(pkg.GoFiles[0], runtime.GOROOT())) || pkg.PkgPath == "unsafe" {
			version := strings.TrimPrefix(strings.Split(runtime.Version(), " ")[0], "go")
			//logrus.Debugf("Using runtime.Version (%s) for version of %q", version, pkg.PkgPath)
			return version
		} else {
			logrus.Infof("Unable to get module version for %q %v", pkg.PkgPath, pkg.GoFiles)
			return fallback
		}
	}
}

// this doesn't really belong here but might as well put it with the rest of the PackageTuple stuff
func MakeFallbackVersion(pkgDir string) string {
	repo, err := git.PlainOpenWithOptions(pkgDir, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return pkgDir
	}
	remotes, err := repo.Remotes()
	if err != nil || len(remotes) == 0 {
		return pkgDir
	}
	head, err := repo.Head()
	if err != nil {
		return pkgDir
	}
	url := remotes[0].Config().URLs[0]
	url = strings.TrimPrefix("https://", url)
	hash := head.Hash().String()[:8]
	return fmt.Sprintf("%s@%s", url, hash)
}

// TODO: should this just be replaced with schema.Package?
type PackageTuple struct {
	Name    string
	Version string
}

func NewPackageTuple(pkg *packages.Package, versionFallback string) PackageTuple {
	return PackageTuple{
		Name:    pkg.PkgPath,
		Version: PkgVersion(pkg, versionFallback),
	}
}

type packageTracker struct {
	status map[PackageTuple]packageStatus
	mtx    sync.Mutex
}

func NewPackageTracker() packageTracker {
	return packageTracker{
		status: make(map[PackageTuple]packageStatus),
	}
}

func (tracker *packageTracker) CheckDone(pkg PackageTuple) bool {
	tracker.mtx.Lock()
	defer tracker.mtx.Unlock()

	return tracker.status[pkg] == Done
}

func (tracker *packageTracker) Checkout(packages []PackageTuple, requester string) {
	tracker.mtx.Lock()
	defer tracker.mtx.Unlock()

	for {
		ok := true
		for _, pkg := range packages {
			if tracker.status[pkg] == InProgress {
				logrus.Debugf("Checkout conflict - %s requesting %v, but it's already in progress", requester, pkg)
				ok = false
				break
			}
		}

		if ok {
			break
		} else {
			tracker.mtx.Unlock()
			time.Sleep(1 * time.Second)
			tracker.mtx.Lock()
		}
	}

	for _, pkg := range packages {
		// Leave done packages as-is
		if tracker.status[pkg] == Pending {
			tracker.status[pkg] = InProgress
		}
	}
}

func (tracker *packageTracker) Done(packages []PackageTuple) {
	tracker.mtx.Lock()
	defer tracker.mtx.Unlock()

	for _, pkg := range packages {
		tracker.status[pkg] = Done
	}
}
