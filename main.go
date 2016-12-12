package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"time"

	"code.cloudfoundry.org/guardian/gardener"
	"code.cloudfoundry.org/guardian/properties"
	"code.cloudfoundry.org/guardian/rundmc"
	"code.cloudfoundry.org/guardian/rundmc/bundlerules"
	"code.cloudfoundry.org/guardian/rundmc/dadoo"
	"code.cloudfoundry.org/guardian/rundmc/depot"
	"code.cloudfoundry.org/guardian/rundmc/goci"
	"code.cloudfoundry.org/guardian/rundmc/preparerootfs"
	"code.cloudfoundry.org/guardian/rundmc/runrunc"
	"code.cloudfoundry.org/guardian/rundmc/stopper"
	"code.cloudfoundry.org/lager"

	"github.com/cloudfoundry/gunk/command_runner/linux_command_runner"
	"github.com/eapache/go-resiliency/retrier"
	"github.com/julz/kardigan/crier"
	uuid "github.com/nu7hatch/gouuid"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pivotal-golang/clock"

	"k8s.io/kubernetes/pkg/kubelet/api/v1alpha1/runtime"

	"google.golang.org/grpc"
)

var socketPath = flag.String("socket-path", "/var/run/kardigan.sock", "path to the socket to listen on")
var depotPath = flag.String("depot-path", "/var/run/garden/depot", "path to the depot directory where bundles are stored")
var runcPath = flag.String("oci-runtime-path", "/usr/local/bin/runc", "path to oci runtime executable")

var propsPath = flag.String("props-path", "/var/run/garden/props.json", "path to store container properties")

var initPath = flag.String("init-path", "", "path to init binary to bind-mount in as init process")
var dadooPath = flag.String("shim-path", "", "path to shim binary")
var nstarPath = flag.String("nstar-path", "", "path to nstar binary for stream-in function")
var tarPath = flag.String("tar-path", "", "path to tar binary for stream-in function")

var appArmorProfile = flag.String("apparmor-profile", "", "if set, the appArmor profile to use for unprivileged containers")

func main() {
	flag.Parse()

	l, err := net.Listen("unix", *socketPath)
	if err != nil {
		log.Fatal(err)
	}

	setupCgroups("/sys/fs/cgroup")

	fmt.Println("kardigan is open for e-business")

	s := grpc.NewServer()
	props, err := properties.Load(*propsPath)
	if err != nil {
		log.Fatal(err)
	}

	runtime.RegisterRuntimeServiceServer(s, crier.New(wireContainerizer(props)))
	if err := s.Serve(l); err != nil {
		log.Fatal(err)
	}
}

func setupCgroups(cgroupsMountpoint string) {
	if err := rundmc.NewStarter(lager.NewLogger(""), mustOpen("/proc/cgroups"), mustOpen("/proc/self/cgroup"), cgroupsMountpoint, linux_command_runner.New()).Start(); err != nil {
		log.Fatal(err)
	}
}

func wireContainerizer(properties gardener.PropertyManager) *rundmc.Containerizer {
	depot := depot.New(*depotPath)

	commandRunner := linux_command_runner.New()
	chrootMkdir := bundlerules.ChrootMkdir{
		Command:       preparerootfs.Command,
		CommandRunner: commandRunner,
	}

	pidFileReader := &dadoo.PidFileReader{
		Clock:         clock.NewClock(),
		Timeout:       10 * time.Second,
		SleepInterval: time.Millisecond * 100,
	}

	uidGen := gardener.UidGeneratorFunc(func() string { return mustStringify(uuid.NewV4()) })
	runcrunner := runrunc.New(
		commandRunner,
		runrunc.NewLogRunner(commandRunner, runrunc.LogDir(os.TempDir()).GenerateLogFile),
		goci.RuncBinary(*runcPath),
		*dadooPath,
		*runcPath,
		runrunc.NewExecPreparer(&goci.BndlLoader{}, runrunc.LookupFunc(runrunc.LookupUser), chrootMkdir, NonRootMaxCaps),
		dadoo.NewExecRunner(
			*dadooPath,
			*runcPath,
			uidGen,
			pidFileReader,
			linux_command_runner.New()),
	)

	mounts := []specs.Mount{
		{Type: "sysfs", Source: "sysfs", Destination: "/sys", Options: []string{"nosuid", "noexec", "nodev", "ro"}},
		{Type: "tmpfs", Source: "tmpfs", Destination: "/dev/shm"},
		{Type: "devpts", Source: "devpts", Destination: "/dev/pts",
			Options: []string{"nosuid", "noexec", "newinstance", "ptmxmode=0666", "mode=0620"}},
		{Type: "bind", Source: *initPath, Destination: "/tmp/garden-init", Options: []string{"bind"}},
	}

	privilegedMounts := append(mounts,
		specs.Mount{Type: "proc", Source: "proc", Destination: "/proc", Options: []string{"nosuid", "noexec", "nodev"}},
	)

	unprivilegedMounts := append(mounts,
		specs.Mount{Type: "proc", Source: "proc", Destination: "/proc", Options: []string{"nosuid", "noexec", "nodev"}},
	)

	rwm := "rwm"
	character := "c"
	var majorMinor = func(i int64) *int64 {
		return &i
	}

	denyAll := specs.LinuxDeviceCgroup{Allow: false, Access: &rwm}
	allowedDevices := []specs.LinuxDeviceCgroup{
		{Access: &rwm, Type: &character, Major: majorMinor(1), Minor: majorMinor(3), Allow: true},
		{Access: &rwm, Type: &character, Major: majorMinor(5), Minor: majorMinor(0), Allow: true},
		{Access: &rwm, Type: &character, Major: majorMinor(1), Minor: majorMinor(8), Allow: true},
		{Access: &rwm, Type: &character, Major: majorMinor(1), Minor: majorMinor(9), Allow: true},
		{Access: &rwm, Type: &character, Major: majorMinor(1), Minor: majorMinor(5), Allow: true},
		{Access: &rwm, Type: &character, Major: majorMinor(1), Minor: majorMinor(7), Allow: true},
		{Access: &rwm, Type: &character, Major: majorMinor(1), Minor: majorMinor(7), Allow: true},
	}

	baseProcess := specs.Process{
		Capabilities: UnprivilegedMaxCaps,
		Args:         []string{"/tmp/garden-init"},
		Cwd:          "/",
	}

	rootUID := uint32(100000)
	idMappings := []specs.LinuxIDMapping{
		{ContainerID: 0, HostID: rootUID, Size: 1},
		{ContainerID: 1, HostID: 1, Size: rootUID - 1},
	}

	baseBundle := goci.Bundle().
		WithNamespaces(PrivilegedContainerNamespaces...).
		WithResources(&specs.LinuxResources{Devices: append([]specs.LinuxDeviceCgroup{denyAll}, allowedDevices...)}).
		WithProcess(baseProcess)

	unprivilegedBundle := baseBundle.
		WithNamespace(goci.UserNamespace).
		WithUIDMappings(idMappings...).
		WithGIDMappings(idMappings...).
		WithMounts(unprivilegedMounts...).
		WithMaskedPaths(defaultMaskedPaths())

	unprivilegedBundle.Spec.Linux.Seccomp = seccomp
	if *appArmorProfile != "" {
		unprivilegedBundle.Spec.Process.ApparmorProfile = *appArmorProfile
	}

	privilegedBundle := baseBundle.
		WithMounts(privilegedMounts...).
		WithCapabilities(PrivilegedMaxCaps...)

	template := &rundmc.BundleTemplate{
		Rules: []rundmc.BundlerRule{
			bundlerules.Base{
				PrivilegedBase:   privilegedBundle,
				UnprivilegedBase: unprivilegedBundle,
			},
			bundlerules.RootFS{
				ContainerRootUID: int(rootUID),
				ContainerRootGID: int(rootUID),
				MkdirChown:       chrootMkdir,
			},
			bundlerules.Limits{},
			bundlerules.BindMounts{},
			bundlerules.Env{},
			bundlerules.Hostname{},
			&podRule{},
		},
	}

	eventStore := rundmc.NewEventStore(properties)
	stateStore := rundmc.NewStateStore(properties)

	nstar := rundmc.NewNstarRunner(*nstarPath, *tarPath, linux_command_runner.New())
	stopper := stopper.New(stopper.NewRuncStateCgroupPathResolver("/run/runc"), nil, retrier.New(retrier.ConstantBackoff(10, 1*time.Second), nil))
	return rundmc.New(depot, template, runcrunner, &goci.BndlLoader{}, nstar, stopper, eventStore, stateStore)
}

func defaultMaskedPaths() []string {
	return []string{
		"/proc/kcore",
		"/proc/latency_stats",
		"/proc/timer_stats",
		"/proc/sched_debug",
	}
}

func mustStringify(s interface{}, e error) string {
	if e != nil {
		panic(e)
	}

	return fmt.Sprintf("%s", s)
}

func mustOpen(path string) io.ReadCloser {
	if r, err := os.Open(path); err != nil {
		panic(err)
	} else {
		return r
	}
}

type podRule struct{}

func (p podRule) Apply(bndl goci.Bndl, spec gardener.DesiredContainerSpec) goci.Bndl {
	for ns, path := range spec.NsPath {
		bndl = bndl.WithNamespace(specs.LinuxNamespace{
			Type: ns,
			Path: path,
		})
	}

	if spec.Process != nil {
		bndl = bndl.WithProcess(*spec.Process)
	}

	return bndl
}
