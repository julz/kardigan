package crier

import (
	"fmt"
	"io/ioutil"
	"path/filepath"
	"strconv"

	"code.cloudfoundry.org/guardian/gardener"
	"code.cloudfoundry.org/lager"

	"golang.org/x/net/context"

	uuid "github.com/nu7hatch/gouuid"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"k8s.io/kubernetes/pkg/kubelet/api/v1alpha1/runtime"
)

const sandboxRootfsPath = "/opt/warden/rootfs"

// crier implements the Kubernetes CRI interfaces using garden components
type crier struct {
	ctzr gardener.Containerizer
}

// New creates a new RuntimeServiceServer using garden components
func New(ctzr gardener.Containerizer) runtime.RuntimeServiceServer {
	return &crier{
		ctzr: ctzr,
	}
}

func (c *crier) Version(context.Context, *runtime.VersionRequest) (*runtime.VersionResponse, error) {
	name := "kardigan"
	version := "0.0.0"
	return &runtime.VersionResponse{
		RuntimeName:    &name,
		RuntimeVersion: &version,
	}, nil
}

func (c *crier) RunPodSandbox(ctx context.Context, req *runtime.RunPodSandboxRequest) (*runtime.RunPodSandboxResponse, error) {
	v4, err := uuid.NewV4()
	if err != nil {
		return nil, err
	}

	id := v4.String()
	if err := c.ctzr.Create(lager.NewLogger(""), gardener.DesiredContainerSpec{
		Handle:     id,
		RootFSPath: sandboxRootfsPath,
		Hostname:   req.GetConfig().GetHostname(),
	}); err != nil {
		return nil, err
	}

	return &runtime.RunPodSandboxResponse{
		PodSandboxId: &id,
	}, nil
}

func (c *crier) StopPodSandbox(ctx context.Context, req *runtime.StopPodSandboxRequest) (*runtime.StopPodSandboxResponse, error) {
	return &runtime.StopPodSandboxResponse{}, c.ctzr.Stop(lager.NewLogger(""), req.GetPodSandboxId(), true)
}

func (c *crier) RemovePodSandbox(ctx context.Context, req *runtime.RemovePodSandboxRequest) (*runtime.RemovePodSandboxResponse, error) {
	return &runtime.RemovePodSandboxResponse{}, c.ctzr.Destroy(lager.NewLogger(""), req.GetPodSandboxId())
}

func (c *crier) PodSandboxStatus(context.Context, *runtime.PodSandboxStatusRequest) (*runtime.PodSandboxStatusResponse, error) {
	panic("not implemented")
}

func (c *crier) ListPodSandbox(context.Context, *runtime.ListPodSandboxRequest) (*runtime.ListPodSandboxResponse, error) {
	panic("not implemented")
}

func (c *crier) CreateContainer(ctx context.Context, req *runtime.CreateContainerRequest) (*runtime.CreateContainerResponse, error) {
	v4, err := uuid.NewV4()
	if err != nil {
		return nil, err
	}

	sandbox, err := c.ctzr.Info(lager.NewLogger(""), req.GetPodSandboxId())
	if err != nil {
		return nil, err
	}

	id := v4.String()
	cwd := "/"
	if cwd == "" {
		cwd = req.GetConfig().GetWorkingDir()
	}

	if err := c.ctzr.Create(lager.NewLogger(""), gardener.DesiredContainerSpec{
		Handle:     id,
		RootFSPath: sandboxRootfsPath,
		NsPath: map[specs.LinuxNamespaceType]string{
			specs.NetworkNamespace: fmt.Sprintf("/proc/%d/ns/net", sandbox.Pid),
			specs.UserNamespace:    fmt.Sprintf("/proc/%d/ns/user", sandbox.Pid),
		},
		Process: &specs.Process{
			Args: req.GetConfig().GetCommand(),
			Cwd:  cwd,
		},
	}); err != nil {
		return nil, err
	}

	return &runtime.CreateContainerResponse{
		ContainerId: &id,
	}, nil
}

func (c *crier) StartContainer(ctx context.Context, req *runtime.StartContainerRequest) (*runtime.StartContainerResponse, error) {
	err := c.ctzr.Start(lager.NewLogger(""), req.GetContainerId())
	return &runtime.StartContainerResponse{}, err
}

func (c *crier) StopContainer(context.Context, *runtime.StopContainerRequest) (*runtime.StopContainerResponse, error) {
	panic("not implemented")
}

func (c *crier) RemoveContainer(context.Context, *runtime.RemoveContainerRequest) (*runtime.RemoveContainerResponse, error) {
	panic("not implemented")
}

func (c *crier) ListContainers(context.Context, *runtime.ListContainersRequest) (*runtime.ListContainersResponse, error) {
	panic("not implemented")
}

func (c *crier) ContainerStatus(ctx context.Context, req *runtime.ContainerStatusRequest) (*runtime.ContainerStatusResponse, error) {
	info, err := c.ctzr.Info(lager.NewLogger(""), req.GetContainerId())
	if err != nil {
		return nil, err
	}

	/// mwahahack
	var exit *int32 = nil
	if b, err := ioutil.ReadFile(filepath.Join(info.BundlePath, "exitcode")); err == nil {
		if x, err := strconv.Atoi(string(b)); err == nil {
			t := int32(x)
			exit = &t
		}
	}

	return &runtime.ContainerStatusResponse{
		Status: &runtime.ContainerStatus{
			ExitCode: exit,
		},
	}, nil
}

func (c *crier) ExecSync(context.Context, *runtime.ExecSyncRequest) (*runtime.ExecSyncResponse, error) {
	panic("not implemented")
}

func (c *crier) Exec(context.Context, *runtime.ExecRequest) (*runtime.ExecResponse, error) {
	panic("not implemented")
}

func (c *crier) Attach(context.Context, *runtime.AttachRequest) (*runtime.AttachResponse, error) {
	panic("not implemented")
}

func (c *crier) PortForward(context.Context, *runtime.PortForwardRequest) (*runtime.PortForwardResponse, error) {
	panic("not implemented")
}

func (c *crier) UpdateRuntimeConfig(context.Context, *runtime.UpdateRuntimeConfigRequest) (*runtime.UpdateRuntimeConfigResponse, error) {
	panic("not implemented")
}

func (c *crier) Status(context.Context, *runtime.StatusRequest) (*runtime.StatusResponse, error) {
	panic("not implemented")
}
