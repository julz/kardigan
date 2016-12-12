package test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"google.golang.org/grpc"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"

	"k8s.io/kubernetes/pkg/kubelet/api/v1alpha1/runtime"
)

var _ = Describe("CRI", func() {
	var (
		workDir string
		sess    *gexec.Session
		conn    *grpc.ClientConn
		client  runtime.RuntimeServiceClient
	)

	BeforeEach(func() {
		kardigan, err := gexec.Build("github.com/julz/kardigan")
		Expect(err).NotTo(HaveOccurred())

		dadoo, err := gexec.Build("github.com/julz/kardigan/cmd/dadoo")
		Expect(err).NotTo(HaveOccurred())

		workDir := tmp("critest0")
		sockPath := filepath.Join(workDir, "stripey.sock")

		sess, err = gexec.Start(exec.Command(kardigan, "--socket-path", sockPath, "--init-path", "/bin/true", "--shim-path", dadoo), GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())

		Eventually(sess).Should(gbytes.Say("open for e-business"))

		conn, err = grpc.Dial(sockPath, grpc.WithInsecure(), grpc.WithDialer(func(a string, t time.Duration) (net.Conn, error) {
			return net.DialTimeout("unix", a, t)
		}))

		Expect(err).NotTo(HaveOccurred())
		client = runtime.NewRuntimeServiceClient(conn)
	})

	AfterEach(func() {
		Expect(conn.Close()).To(Succeed())
		Eventually(sess.Kill()).Should(gexec.Exit())
		Expect(os.RemoveAll(workDir)).To(Succeed())
	})

	Describe("Getting version", func() {
		It("can get the version", func() {
			resp, err := client.Version(context.TODO(), &runtime.VersionRequest{})
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.GetRuntimeName()).To(Equal("kardigan"))
		})
	})

	Describe("Creating a Pod Sandbox", func() {
		var (
			resp *runtime.RunPodSandboxResponse
		)

		BeforeEach(func() {
			var err error
			resp, err = client.RunPodSandbox(context.TODO(), &runtime.RunPodSandboxRequest{
				Config: &runtime.PodSandboxConfig{
					Metadata: &runtime.PodSandboxMetadata{
						Name:      s("potato"),
						Uid:       s("po-tat-o"),
						Namespace: s("vegetable"),
					},

					Hostname: s("the-hostname"),
				},
			})

			Expect(err).NotTo(HaveOccurred())
		})

		It("creates a container to hold the requested namespaces", func() {
			sess, err := gexec.Start(exec.Command("runc", "state", resp.GetPodSandboxId()), GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			Eventually(sess).Should(gexec.Exit(0))
		})

		It("ensures the container has the requested hostname", func() {
			sess, err := gexec.Start(exec.Command("runc", "exec", resp.GetPodSandboxId(), "/bin/sh", "-c", "'hostname'"), GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			Eventually(sess).Should(gexec.Exit(0))

			Expect(sess).To(gbytes.Say("the-hostname"))
		})

		It("destroys the container properly", func() {
			sess, err := gexec.Start(exec.Command("runc", "state", resp.GetPodSandboxId()), GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			Eventually(sess).Should(gexec.Exit(0))

			_, err = client.RemovePodSandbox(context.TODO(), &runtime.RemovePodSandboxRequest{
				PodSandboxId: resp.PodSandboxId,
			})
			Expect(err).NotTo(HaveOccurred())

			sess, err = gexec.Start(exec.Command("runc", "state", resp.GetPodSandboxId()), GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			Eventually(sess).Should(gexec.Exit(1))
			Expect(sess.Err).To(gbytes.Say("does not exist"))
		})

		PIt("can get the sandbox status", func() {})

		PIt("stores sandbox labels and properties correctly", func() {})

		PIt("lists the sandboxes", func() {})

		PIt("filters sandboxes by state and property", func() {})

		Describe("and creating a container inside it", func() {
			var (
				createCtrResp *runtime.CreateContainerResponse
			)

			BeforeEach(func() {
				var err error
				createCtrResp, err = client.CreateContainer(context.TODO(), &runtime.CreateContainerRequest{
					PodSandboxId: resp.PodSandboxId,
					Config: &runtime.ContainerConfig{
						Image: &runtime.ImageSpec{
							Image: s("busybox"),
						},
						Command: []string{"/bin/sh", "-c", "exit 55"},
					},
				})
				Expect(err).NotTo(HaveOccurred())
			})

			It("creates a container", func() {
				sess, err := gexec.Start(exec.Command("runc", "state", createCtrResp.GetContainerId()), GinkgoWriter, GinkgoWriter)
				Expect(err).NotTo(HaveOccurred())
				Eventually(sess).Should(gexec.Exit(0))
			})

			PIt("sets the containers hostname to the same hostname as the pod", func() {
			})

			It("gives the container its own copy of the mnt namespace", func() {
				sandboxMntNs, err := os.Stat(fmt.Sprintf("/proc/%d/ns/mnt", pidOf(resp.GetPodSandboxId())))
				Expect(err).NotTo(HaveOccurred())

				ctrMntNs, err := os.Stat(fmt.Sprintf("/proc/%d/ns/mnt", pidOf(createCtrResp.GetContainerId())))
				Expect(err).NotTo(HaveOccurred())

				Expect(sandboxMntNs.Sys().(*syscall.Stat_t).Ino).NotTo(Equal(ctrMntNs.Sys().(*syscall.Stat_t).Ino))
			})

			It("creates the container in the network namespace of the pod", func() {
				sandboxMntNs, err := os.Stat(fmt.Sprintf("/proc/%d/ns/net", pidOf(resp.GetPodSandboxId())))
				Expect(err).NotTo(HaveOccurred())

				ctrMntNs, err := os.Stat(fmt.Sprintf("/proc/%d/ns/net", pidOf(createCtrResp.GetContainerId())))
				Expect(err).NotTo(HaveOccurred())

				Expect(sandboxMntNs.Sys().(*syscall.Stat_t).Ino).To(Equal(ctrMntNs.Sys().(*syscall.Stat_t).Ino))
			})

			It("starts the requested command and gets the exit status", func() {
				_, err := client.StartContainer(context.TODO(), &runtime.StartContainerRequest{
					ContainerId: createCtrResp.ContainerId,
				})
				Expect(err).NotTo(HaveOccurred())

				Eventually(func() int32 {
					status, err := client.ContainerStatus(context.TODO(), &runtime.ContainerStatusRequest{
						ContainerId: createCtrResp.ContainerId,
					})
					Expect(err).NotTo(HaveOccurred())

					return status.GetStatus().GetExitCode()
				}, 5).Should(BeEquivalentTo(55))
			})
		})
	})
})

func pidOf(id string) int {
	sess, err := gexec.Start(exec.Command("runc", "state", id), GinkgoWriter, GinkgoWriter)
	Expect(err).NotTo(HaveOccurred())
	Eventually(sess).Should(gexec.Exit(0))

	state := struct {
		Pid int `json:"pid"`
	}{}
	Expect(json.NewDecoder(bytes.NewReader(sess.Out.Contents())).Decode(&state)).To(Succeed())
	return state.Pid
}

func tmp(prefix string) string {
	t, err := ioutil.TempDir("", prefix)
	Expect(err).NotTo(HaveOccurred())
	return t
}

func s(s string) *string { return &s }
