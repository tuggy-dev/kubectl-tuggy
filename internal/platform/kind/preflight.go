package kind

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strconv"
	"time"

	"github.com/tuggy-dev/kubectl-tuggy/internal/platform"
)

// Preflight checks, before anything is created, that the container engine
// works, the docker CLI the kind library drives is installed, no kind
// cluster with the same name exists, and the requested host ports and mount
// paths are usable. Each failure carries a fix.
func (p *Platform) Preflight(ctx context.Context, plan *platform.Plan) []platform.CheckResult {
	vars, ok := plan.Variables.(Variables)
	if !ok {
		return []platform.CheckResult{{Name: "kind plan", Status: platform.CheckFail, Message: "plan was not made by the kind platform"}}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()

	var results []platform.CheckResult
	add := func(r platform.CheckResult) { results = append(results, r) }

	engine := p.checkEngine(ctx)
	add(engine)
	cli := p.checkCLI()
	add(cli)
	if engine.Status == platform.CheckPass && cli.Status == platform.CheckPass {
		add(p.checkName(vars))
	}
	for _, r := range p.checkPorts(vars) {
		add(r)
	}
	for _, r := range p.checkMounts(vars) {
		add(r)
	}
	return results
}

func (p *Platform) checkEngine(ctx context.Context) platform.CheckResult {
	const name = "Container engine"
	r, err := p.NewRunner()
	if err == nil {
		err = r.Ping(ctx)
	}
	if err != nil {
		return fail(name, err.Error(), "Start Docker Desktop, Colima, Rancher Desktop or another Docker-compatible engine, and check that `docker ps` works")
	}
	return pass(name, "reachable")
}

// checkCLI checks for the docker command: the kind library drives the
// engine through it rather than through the API.
func (p *Platform) checkCLI() platform.CheckResult {
	const name = "docker CLI"
	path, err := p.LookPath("docker")
	if err != nil {
		return fail(name, "not found on PATH; kind runs the docker command to manage nodes",
			"Install the Docker CLI (it comes with Docker Desktop) and make sure `docker version` works")
	}
	return pass(name, path)
}

// checkName fails if a kind cluster with the tuggy name already exists, for
// example one left by an earlier tool run, since kind would refuse to
// create it.
func (p *Platform) checkName(vars Variables) platform.CheckResult {
	name := "Cluster name " + vars.ClusterName
	existing, err := p.NewProvider(newProgressLogger(nil, nil)).List()
	if err != nil {
		return platform.CheckResult{Name: name, Status: platform.CheckWarn, Message: "could not list kind clusters: " + err.Error()}
	}
	if slices.Contains(existing, vars.ClusterName) {
		return fail(name, "a kind cluster with this name already exists",
			fmt.Sprintf("If tuggy made it, run: kubectl tuggy delete cluster %s. Otherwise delete it with: kind delete cluster --name %s, or pick another name", vars.Name, vars.ClusterName))
	}
	return pass(name, "available")
}

// checkPorts fails for fixed host ports that are already in use.
func (p *Platform) checkPorts(vars Variables) []platform.CheckResult {
	var results []platform.CheckResult
	if port := vars.APIServerPort; port != 0 {
		name := "API server port " + strconv.Itoa(int(port))
		if p.PortFree("127.0.0.1", port) {
			results = append(results, pass(name, "free"))
		} else {
			results = append(results, fail(name, "already in use", "Pick another port with apiServerPort (--api-server-port), or 0 for a random free one"))
		}
	}
	for i, pm := range vars.ExtraPortMappings {
		if pm.HostPort == 0 || pm.Protocol != "TCP" {
			continue
		}
		name := fmt.Sprintf("Host port %d", pm.HostPort)
		if p.PortFree(pm.ListenAddress, pm.HostPort) {
			results = append(results, pass(name, "free"))
		} else {
			results = append(results, fail(name, "already in use",
				fmt.Sprintf("Pick another hostPort for extraPortMappings[%d]", i)))
		}
	}
	return results
}

// checkMounts fails for mount paths that don't exist on this computer.
func (p *Platform) checkMounts(vars Variables) []platform.CheckResult {
	var results []platform.CheckResult
	for i, m := range vars.ExtraMounts {
		name := "Mount " + m.HostPath
		if _, err := p.Stat(m.HostPath); err != nil {
			results = append(results, fail(name, "does not exist on this computer",
				fmt.Sprintf("Create the directory, or fix extraMounts[%d].hostPath", i)))
			continue
		}
		results = append(results, pass(name, "visible in the nodes at "+m.ContainerPath))
	}
	return results
}

// portFree reports whether a TCP port can be listened on at address (all
// addresses when empty).
func portFree(address string, port int32) bool {
	l, err := net.Listen("tcp", net.JoinHostPort(address, strconv.Itoa(int(port))))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

func pass(name, msg string) platform.CheckResult {
	return platform.CheckResult{Name: name, Status: platform.CheckPass, Message: msg}
}

func fail(name, msg, fix string) platform.CheckResult {
	return platform.CheckResult{Name: name, Status: platform.CheckFail, Message: msg, Fix: fix}
}
