package projectconfig

import (
	"fmt"
	"strings"
)

// t2git is a git source with a connection, for services that need one.
const t2git = "[services.web.git]\nconnection = \"gh\"\nrepo = \"acme/web\"\nbranch = \"main\"\n"

// t2watch renders a watch_paths line of n distinct patterns.
func t2watch(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf("\"p%d/**\"", i)
	}
	return "watch_paths = [" + strings.Join(parts, ", ") + "]\n"
}

func t2SectionCases() []t2case {
	return []t2case{
		// health checks
		{name: "health exec without CMD", toml: t2doc(t2web + "[services.web.health_check]\ntype = \"exec\"\ncommand = [\"true\"]\n"), code: CodeInvalidValue, path: "services.web.health_check.command", line: "command ="},
		{name: "health exec with CMD", toml: t2doc(t2web + "[services.web.health_check]\ntype = \"exec\"\ncommand = [\"CMD\", \"true\"]\n"), code: CodeInvalidValue, absent: true},
		{name: "health exec with CMD-SHELL", toml: t2doc(t2web + "[services.web.health_check]\ntype = \"exec\"\ncommand = [\"CMD-SHELL\", \"true\"]\n"), code: CodeInvalidValue, absent: true},
		{name: "health http without port", toml: t2doc("[services.w]\nimage = \"nginx:1\"\n\n[services.w.health_check]\ntype = \"http\"\n"), code: CodeInvalidValue, path: "services.w.health_check.type"},
		{name: "health http with health_check.port", toml: t2doc("[services.w]\nimage = \"nginx:1\"\n\n[services.w.health_check]\ntype = \"http\"\nport = 8080\n"), code: CodeInvalidValue, absent: true},
		{name: "health command with non-exec type", toml: t2doc(t2web + "[services.web.health_check]\ntype = \"http\"\ncommand = [\"CMD\", \"true\"]\n"), code: CodeInvalidValue, path: "services.web.health_check.command"},
		{name: "health path without slash", toml: t2doc(t2web + "[services.web.health_check]\npath = \"healthz\"\n"), code: CodeInvalidValue, path: "services.web.health_check.path"},
		{name: "health deadline too large", toml: t2doc(t2web + "[services.web.health_check]\ndeadline_seconds = 5000\n"), code: CodeInvalidValue, path: "services.web.health_check.deadline_seconds"},
		{name: "health unknown type", toml: t2doc(t2web + "[services.web.health_check]\ntype = \"ping\"\n"), code: CodeInvalidValue, path: "services.web.health_check.type"},
		{name: "health valid", toml: t2doc(t2web + "[services.web.health_check]\ntype = \"http\"\npath = \"/h\"\nport = 80\ninterval_seconds = 1\ntimeout_seconds = 2\nstart_period_seconds = 0\ndeadline_seconds = 3600\nsuccess_threshold = 1\n"), code: CodeInvalidValue, absent: true},

		// hooks
		{name: "hook timeout without command", toml: t2doc(t2web + "[services.web.pre_deploy]\ntimeout_seconds = 30\n"), code: CodeMissingRequired, path: "services.web.pre_deploy.command"},
		{name: "hook empty table", toml: t2doc(t2web + "[services.web.post_deploy]\n"), code: CodeMissingRequired, path: "services.web.post_deploy.command"},
		{name: "hook blank program", toml: t2doc(t2web + "[services.web.pre_deploy]\ncommand = [\"\"]\n"), code: CodeInvalidValue, path: "services.web.pre_deploy.command"},
		{name: "hook timeout out of range", toml: t2doc(t2web + "[services.web.pre_deploy]\ncommand = [\"migrate\"]\ntimeout_seconds = 4000\n"), code: CodeInvalidValue, path: "services.web.pre_deploy.timeout_seconds"},
		{name: "hook valid", toml: t2doc(t2web + "[services.web.pre_deploy]\ncommand = [\"migrate\"]\ntimeout_seconds = 300\n"), code: CodeInvalidValue, absent: true},
		{name: "one_shot with pre_deploy", toml: t2doc("[services.w]\nimage = \"nginx:1\"\none_shot = true\n\n[services.w.pre_deploy]\ncommand = [\"migrate\"]\n"), code: CodeInvalidValue, path: "services.w.one_shot"},
		{name: "one_shot with port", toml: t2doc("[services.w]\nimage = \"nginx:1\"\nport = 80\none_shot = true\n"), code: CodeInvalidValue, path: "services.w.port"},
		{name: "one_shot alone", toml: t2doc("[services.w]\nimage = \"nginx:1\"\none_shot = true\n"), code: CodeInvalidValue, absent: true},

		// git
		{name: "watch_paths bad pattern", toml: t2doc("[services.web]\n\n[services.web.git]\nconnection = \"gh\"\nrepo = \"acme/web\"\nbranch = \"main\"\nwatch_paths = [\"apps/[\"]\n"), code: CodeInvalidValue, path: "services.web.git.watch_paths", line: "watch_paths"},
		{name: "watch_paths parent segment", toml: t2doc("[services.web]\n\n[services.web.git]\nconnection = \"gh\"\nrepo = \"acme/web\"\nbranch = \"main\"\nwatch_paths = [\"../outside\"]\n"), code: CodeInvalidValue, path: "services.web.git.watch_paths"},
		{name: "watch_paths 51 entries", toml: t2doc("[services.web]\n\n[services.web.git]\nconnection = \"gh\"\nrepo = \"acme/web\"\nbranch = \"main\"\n" + t2watch(51)), code: CodeLimitExceeded, path: "services.web.git.watch_paths"},
		{name: "watch_paths 50 entries", toml: t2doc("[services.web]\n\n[services.web.git]\nconnection = \"gh\"\nrepo = \"acme/web\"\nbranch = \"main\"\n" + t2watch(50)), code: CodeLimitExceeded, absent: true},
		{name: "watch_paths valid globs", toml: t2doc("[services.web]\n\n[services.web.git]\nconnection = \"gh\"\nrepo = \"acme/web\"\nbranch = \"main\"\nwatch_paths = [\"apps/web/**\", \"packages/*/src\", \"!**/*.md\", \"Dockerfile\"]\n"), code: CodeInvalidValue, absent: true},
		{name: "watch_paths needs connection", toml: t2doc("[services.web]\n\n[services.web.git]\nurl = \"https://github.com/acme/web.git\"\nbranch = \"main\"\nwatch_paths = [\"a/**\"]\n"), code: CodeInvalidValue, path: "services.web.git.watch_paths"},
		{name: "git url and connection together", toml: t2doc("[services.web]\n\n[services.web.git]\nurl = \"https://github.com/acme/web.git\"\nconnection = \"gh\"\nrepo = \"acme/web\"\nbranch = \"main\"\n"), code: CodeInvalidValue, path: "services.web.git.url"},
		{name: "git connection without repo", toml: t2doc("[services.web]\n\n[services.web.git]\nconnection = \"gh\"\nbranch = \"main\"\n"), code: CodeMissingRequired, path: "services.web.git"},
		{name: "git repo shape", toml: t2doc("[services.web]\n\n[services.web.git]\nconnection = \"gh\"\nrepo = \"just-a-name\"\nbranch = \"main\"\n"), code: CodeInvalidValue, path: "services.web.git.repo"},
		{name: "git submodules value", toml: t2doc("[services.web]\n\n[services.web.git]\nconnection = \"gh\"\nrepo = \"a/b\"\nbranch = \"main\"\nsubmodules = \"maybe\"\n"), code: CodeInvalidValue, path: "services.web.git.submodules"},
		{name: "build type value", toml: t2doc("[services.web]\n\n" + t2git + "\n[services.web.build]\ntype = \"magic\"\n"), code: CodeInvalidValue, path: "services.web.build.type"},
		{name: "build type starter", toml: t2doc("[services.web]\n\n" + t2git + "\n[services.web.build]\ntype = \"starter:node\"\n"), code: CodeInvalidValue, absent: true},

		// docker
		{name: "docker bad capability", toml: t2doc(t2web + "[services.web.docker]\ncap_add = [\"SYS_MODULE\"]\n"), code: CodeInvalidValue, path: "services.web.docker"},
		{name: "docker NET_ADMIN accepted statically", toml: t2doc(t2web + "[services.web.docker]\ncap_add = [\"NET_ADMIN\"]\n"), code: CodeInvalidValue, absent: true},
		{name: "docker NET_ADMIN is not a static restricted-server error", toml: t2doc(t2web + "[services.web.docker]\ncap_add = [\"NET_ADMIN\"]\n"), code: CodeDockerUnrestricted, absent: true},
		{name: "docker privileged accepted statically", toml: t2doc(t2web + "[services.web.docker]\nprivileged = true\n"), code: CodeInvalidValue, absent: true},
		{name: "docker lower-case capability accepted", toml: t2doc(t2web + "[services.web.docker]\ncap_add = [\"cap_net_bind_service\"]\n"), code: CodeInvalidValue, absent: true},
		{name: "docker bad hostname", toml: t2doc(t2web + "[services.web.docker]\nhostname = \"Bad Host\"\n"), code: CodeInvalidValue, path: "services.web.docker"},
		{name: "docker bad sysctl", toml: t2doc(t2web + "[services.web.docker.sysctls]\n\"kernel.core_pattern\" = \"x\"\n"), code: CodeInvalidValue, path: "services.web.docker"},
		{name: "docker ulimit without hard", toml: t2doc(t2web + "[services.web.docker.ulimits.nofile]\nsoft = 1024\n"), code: CodeMissingRequired, path: "services.web.docker.ulimits.nofile"},
		{name: "docker tmpfs collides with file", toml: t2doc(t2web + "[services.web.docker.tmpfs.\"/etc/a.conf\"]\nsize_mb = 8\n\n[services.web.files.\"/etc/a.conf\"]\ncontent = \"x\"\n"), code: CodeInvalidValue, path: "services.web.docker.tmpfs.\"/etc/a.conf\""},
		{name: "docker read-only root with file off volume", toml: t2doc(t2web + "[services.web.docker]\nread_only_rootfs = true\n\n[services.web.files.\"/etc/a.conf\"]\ncontent = \"x\"\n"), code: CodeInvalidValue, path: "services.web.docker.read_only_rootfs"},
		{name: "docker read-only root with file on volume", toml: t2doc(t2web + "[services.web.docker]\nread_only_rootfs = true\n\n[services.web.volumes]\ndata = \"/data\"\n\n[services.web.files.\"/data/a.conf\"]\ncontent = \"x\"\n"), code: CodeInvalidValue, absent: true},
		{name: "docker device outside /dev", toml: t2doc(t2web + "[[services.web.docker.devices]]\nhost_path = \"/etc/passwd\"\n"), code: CodeInvalidValue, path: "services.web.docker"},
		{name: "docker device without host_path", toml: t2doc(t2web + "[[services.web.docker.devices]]\npermissions = \"rw\"\n"), code: CodeMissingRequired, path: "services.web.docker.devices[0]"},
		{name: "docker gpus incomplete", toml: t2doc(t2web + "[services.web.docker.gpus]\ncount = 1\n"), code: CodeMissingRequired, path: "services.web.docker.gpus"},

		// volumes
		{name: "volume with two replicas", toml: t2doc(t2web + "[services.web.volumes]\ndata = \"/data\"\n\n[services.web.scale]\nreplicas = 2\n"), code: CodeReplicasRequireStateles, path: "services.web.scale.replicas", line: "replicas ="},
		{name: "volume with one replica", toml: t2doc(t2web + "[services.web.volumes]\ndata = \"/data\"\n\n[services.web.scale]\nreplicas = 1\n"), code: CodeReplicasRequireStateles, absent: true},
		{name: "two replicas without volume", toml: t2doc(t2web + "[services.web.scale]\nreplicas = 2\n"), code: CodeReplicasRequireStateles, absent: true},
		{name: "volume with autoscaling max", toml: t2doc(t2web + "[services.web.volumes]\ndata = \"/data\"\n\n[services.web.scale.autoscaling]\nenabled = true\nmin = 1\nmax = 3\n"), code: CodeReplicasRequireStateles, path: "services.web.scale.autoscaling.max"},
		{name: "volume with disabled autoscaling", toml: t2doc(t2web + "[services.web.volumes]\ndata = \"/data\"\n\n[services.web.scale.autoscaling]\nenabled = false\nmin = 1\nmax = 3\n"), code: CodeReplicasRequireStateles, absent: true},
		{name: "volume name upper-case", toml: t2doc(t2web + "[services.web.volumes]\nBig = \"/data\"\n"), code: CodeInvalidValue, path: "services.web.volumes.Big"},
		{name: "volume relative mount", toml: t2doc(t2web + "[services.web.volumes]\ndata = \"data\"\n"), code: CodeInvalidValue, path: "services.web.volumes.data"},
		{name: "volume mounted at root", toml: t2doc(t2web + "[services.web.volumes]\ndata = \"/\"\n"), code: CodeInvalidValue, path: "services.web.volumes.data"},
		{name: "volume mount parent segment", toml: t2doc(t2web + "[services.web.volumes]\ndata = \"/a/../b\"\n"), code: CodeInvalidValue, path: "services.web.volumes.data"},
		{name: "volumes share a mount path", toml: t2doc(t2web + "[services.web.volumes]\na = \"/data\"\nb = \"/data\"\n"), code: CodeInvalidValue, path: "services.web.volumes.b"},
		{name: "volumes valid", toml: t2doc(t2web + "[services.web.volumes]\nuploads = \"/app/uploads\"\ncache_1 = \"/var/cache\"\n"), code: CodeInvalidValue, absent: true},

		// egress
		{name: "egress blocked with networks", toml: t2doc("[services.web]\nimage = \"nginx:1\"\nblock_egress = true\nnetworks = [\"backplane\"]\n"), code: CodeEgressBlocked, path: "services.web.networks", line: "networks ="},
		{name: "egress allowed with networks", toml: t2doc("[services.web]\nimage = \"nginx:1\"\nblock_egress = false\nnetworks = [\"backplane\"]\n"), code: CodeEgressBlocked, absent: true},
		{name: "egress blocked without networks", toml: t2doc("[services.web]\nimage = \"nginx:1\"\nblock_egress = true\n"), code: CodeEgressBlocked, absent: true},
		{name: "egress blocked database with networks", toml: t2doc("[services.db]\nblock_egress = true\nnetworks = [\"backplane\"]\n\n[services.db.database]\nengine = \"postgres\"\n"), code: CodeEgressBlocked, path: "services.db.networks"},

		// scale and uptime bounds
		{name: "scale replicas too high", toml: t2doc(t2web + "[services.web.scale]\nreplicas = 11\n"), code: CodeInvalidValue, path: "services.web.scale.replicas"},
		{name: "scale replicas zero", toml: t2doc(t2web + "[services.web.scale]\nreplicas = 0\n"), code: CodeInvalidValue, path: "services.web.scale.replicas"},
		{name: "scale replicas at the bounds", toml: t2doc(t2web + "[services.web.scale]\nreplicas = 10\n"), code: CodeInvalidValue, absent: true},
		{name: "autoscaling max below min", toml: t2doc(t2web + "[services.web.scale.autoscaling]\nmin = 5\nmax = 2\n"), code: CodeInvalidValue, path: "services.web.scale.autoscaling.max"},
		{name: "autoscaling target cpu low", toml: t2doc(t2web + "[services.web.scale.autoscaling]\ntarget_cpu_percent = 10\n"), code: CodeInvalidValue, path: "services.web.scale.autoscaling.target_cpu_percent"},
		{name: "autoscaling target cpu high", toml: t2doc(t2web + "[services.web.scale.autoscaling]\ntarget_cpu_percent = 96\n"), code: CodeInvalidValue, path: "services.web.scale.autoscaling.target_cpu_percent"},
		{name: "autoscaling valid", toml: t2doc(t2web + "[services.web.scale.autoscaling]\nenabled = true\nmin = 2\nmax = 5\ntarget_cpu_percent = 95\n"), code: CodeInvalidValue, absent: true},
		{name: "uptime failure threshold", toml: t2doc(t2web + "[services.web.uptime]\nfailure_threshold = 21\n"), code: CodeInvalidValue, path: "services.web.uptime.failure_threshold"},
		{name: "uptime recovery threshold zero", toml: t2doc(t2web + "[services.web.uptime]\nrecovery_threshold = 0\n"), code: CodeInvalidValue, path: "services.web.uptime.recovery_threshold"},
		{name: "uptime reminder too long", toml: t2doc(t2web + "[services.web.uptime]\nreminder_minutes = 1441\n"), code: CodeInvalidValue, path: "services.web.uptime.reminder_minutes"},
		{name: "uptime at the bounds", toml: t2doc(t2web + "[services.web.uptime]\nfailure_threshold = 20\nrecovery_threshold = 1\nreminder_minutes = 1440\n"), code: CodeInvalidValue, absent: true},

		// runtime keys
		{name: "stop grace out of range", toml: t2doc("[services.web]\nimage = \"nginx:1\"\nstop_grace_seconds = 4000\n"), code: CodeInvalidValue, path: "services.web.stop_grace_seconds"},
		{name: "image retention out of range", toml: t2doc("[services.web]\nimage = \"nginx:1\"\nimage_retention = 51\n"), code: CodeInvalidValue, path: "services.web.image_retention"},
		{name: "cpus negative", toml: t2doc("[services.web]\nimage = \"nginx:1\"\ncpus = -1.0\n"), code: CodeInvalidValue, path: "services.web.cpus"},
		{name: "image with whitespace", toml: t2doc("[services.web]\nimage = \"nginx 1\"\n"), code: CodeInvalidValue, path: "services.web.image"},
		{name: "command blank program", toml: t2doc("[services.web]\nimage = \"nginx:1\"\ncommand = [\"\"]\n"), code: CodeInvalidValue, path: "services.web.command"},
		{name: "command empty clears", toml: t2doc("[services.web]\nimage = \"nginx:1\"\ncommand = []\n"), code: CodeInvalidValue, absent: true},
		{name: "name too long", toml: t2doc("[services.web]\nimage = \"nginx:1\"\nname = \"" + strings.Repeat("n", 121) + "\"\n"), code: CodeInvalidValue, path: "services.web.name"},
		{name: "name at the limit", toml: t2doc("[services.web]\nimage = \"nginx:1\"\nname = \"" + strings.Repeat("n", 120) + "\"\n"), code: CodeInvalidValue, absent: true},
		{name: "empty server name", toml: t2doc("[services.web]\nimage = \"nginx:1\"\nserver = \"\"\n"), code: CodeInvalidValue, path: "services.web.server"},
		{name: "duplicate depends_on", toml: t2doc("[services.web]\nimage = \"nginx:1\"\ndepends_on = [\"db\", \"db\"]\n\n[services.db]\nimage = \"postgres:17\"\n"), code: CodeInvalidValue, path: "services.web.depends_on[1]"},
		{name: "only_in inside an override", toml: t2doc(t2web + "[environments.staging.services.web]\nonly_in = [\"staging\"]\n"), code: CodeInvalidValue, path: "environments.staging.services.web.only_in"},
		{name: "only_in with an invalid slug", toml: t2doc("[services.web]\nimage = \"nginx:1\"\nonly_in = [\"Prod\"]\n"), code: CodeInvalidSlug, path: "services.web.only_in[0]"},
		{name: "only_in valid", toml: t2doc("[services.web]\nimage = \"nginx:1\"\nonly_in = [\"production\", \"staging\"]\n"), code: CodeInvalidSlug, absent: true},

		// cron
		{name: "task schedule invalid", toml: t2doc(t2web + "[services.web.tasks.job]\nschedule = \"every day\"\ncommand = [\"run\"]\n"), code: CodeInvalidValue, path: "services.web.tasks.job.schedule", line: "schedule ="},
		{name: "task timezone invalid", toml: t2doc(t2web + "[services.web.tasks.job]\nschedule = \"0 7 * * *\"\ntimezone = \"Mars/Base\"\ncommand = [\"run\"]\n"), code: CodeInvalidValue, path: "services.web.tasks.job.timezone"},
		{name: "task timezone valid", toml: t2doc(t2web + "[services.web.tasks.job]\nschedule = \"0 7 * * 1-5\"\ntimezone = \"Europe/Berlin\"\ncommand = [\"run\"]\n"), code: CodeInvalidValue, absent: true},
		{name: "task alias schedule", toml: t2doc(t2web + "[services.web.tasks.job]\nschedule = \"@daily\"\ncommand = [\"run\"]\n"), code: CodeInvalidValue, absent: true},
		{name: "task every minute is fine", toml: t2doc(t2web + "[services.web.tasks.job]\nschedule = \"* * * * *\"\ncommand = [\"run\"]\n"), code: CodeInvalidValue, absent: true},
		{name: "task without command", toml: t2doc(t2web + "[services.web.tasks.job]\nschedule = \"0 7 * * *\"\n"), code: CodeMissingRequired, path: "services.web.tasks.job.command"},
		{name: "task without schedule", toml: t2doc(t2web + "[services.web.tasks.job]\ncommand = [\"run\"]\n"), code: CodeMissingRequired, path: "services.web.tasks.job.schedule"},
		{name: "task timeout too long", toml: t2doc(t2web + "[services.web.tasks.job]\nschedule = \"0 7 * * *\"\ncommand = [\"run\"]\ntimeout_seconds = 90000\n"), code: CodeInvalidValue, path: "services.web.tasks.job.timeout_seconds"},
		{name: "task concurrency value", toml: t2doc(t2web + "[services.web.tasks.job]\nschedule = \"0 7 * * *\"\ncommand = [\"run\"]\nconcurrency = \"replace\"\n"), code: CodeInvalidValue, path: "services.web.tasks.job.concurrency"},
		{name: "task name invalid", toml: t2doc(t2web + "[services.web.tasks.\"Bad Name\"]\nschedule = \"0 7 * * *\"\ncommand = [\"run\"]\n"), code: CodeInvalidValue, path: "services.web.tasks.\"Bad Name\""},

		{name: "process without command", toml: t2doc(t2web + "[services.web.processes.worker]\nstop_grace_seconds = 5\n"), code: CodeProcessCommandRequired, path: "services.web.processes.worker.command"},
		{name: "process with empty command", toml: t2doc(t2web + "[services.web.processes.worker]\ncommand = []\n"), code: CodeProcessCommandRequired, path: "services.web.processes.worker.command"},
		{name: "process with command", toml: t2doc(t2web + "[services.web.processes.worker]\ncommand = [\"run\", \"worker\"]\n"), code: CodeProcessCommandRequired, absent: true},
		{name: "process command with empty element", toml: t2doc(t2web + "[services.web.processes.worker]\ncommand = [\"run\", \"\"]\n"), code: CodeInvalidValue, path: "services.web.processes.worker.command"},
		{name: "process named like the service's own process", toml: t2doc(t2web + "[services.web.processes.web]\ncommand = [\"run\"]\n"), code: CodeProcessNameReserved, path: "services.web.processes.web", line: "processes.web"},
		{name: "process named like the renamed own process", toml: t2doc("[services.web]\nimage = \"nginx:1\"\nport = 80\nprocess_name = \"app\"\n\n[services.web.processes.app]\ncommand = [\"run\"]\n"), code: CodeProcessNameReserved, path: "services.web.processes.app"},
		{name: "process web is free when the own process is renamed", toml: t2doc("[services.web]\nimage = \"nginx:1\"\nport = 80\nprocess_name = \"app\"\n\n[services.web.processes.web]\ncommand = [\"run\"]\n"), code: CodeProcessNameReserved, absent: true},
		{name: "process name upper-case", toml: t2doc(t2web + "[services.web.processes.Bad_Name]\ncommand = [\"run\"]\n"), code: CodeInvalidValue, path: "services.web.processes.Bad_Name"},
		{name: "process name too long", toml: t2doc(t2web + "[services.web.processes." + strings.Repeat("a", 32) + "]\ncommand = [\"run\"]\n"), code: CodeInvalidValue},
		{name: "process name at the length limit", toml: t2doc(t2web + "[services.web.processes." + strings.Repeat("a", 31) + "]\ncommand = [\"run\"]\n"), code: CodeInvalidValue, absent: true},
		{name: "process_name invalid", toml: t2doc("[services.web]\nimage = \"nginx:1\"\nprocess_name = \"Bad Name\"\n"), code: CodeInvalidValue, path: "services.web.process_name", line: "process_name ="},
		{name: "process_name valid", toml: t2doc("[services.web]\nimage = \"nginx:1\"\nprocess_name = \"api\"\n"), code: CodeInvalidValue, absent: true},
		{name: "process port conflicts with the service port", toml: t2doc(t2web + "[services.web.processes.admin]\ncommand = [\"run\"]\nport = 80\n"), code: CodeProcessPortConflict, path: "services.web.processes.admin.port"},
		{name: "two processes share a port", toml: t2doc(t2web + "[services.web.processes.a]\ncommand = [\"run\"]\nport = 81\n\n[services.web.processes.b]\ncommand = [\"run\"]\nport = 81\n"), code: CodeProcessPortConflict, path: "services.web.processes.b.port"},
		{name: "processes with distinct ports", toml: t2doc(t2web + "[services.web.processes.a]\ncommand = [\"run\"]\nport = 81\n\n[services.web.processes.b]\ncommand = [\"run\"]\nport = 82\n"), code: CodeProcessPortConflict, absent: true},
		{name: "processes without ports never conflict", toml: t2doc(t2web + "[services.web.processes.a]\ncommand = [\"run\"]\n\n[services.web.processes.b]\ncommand = [\"run\"]\n"), code: CodeProcessPortConflict, absent: true},
		{name: "process port range", toml: t2doc(t2web + "[services.web.processes.a]\ncommand = [\"run\"]\nport = 70000\n"), code: CodeInvalidValue, path: "services.web.processes.a.port"},
		{name: "process stop grace range", toml: t2doc(t2web + "[services.web.processes.a]\ncommand = [\"run\"]\nstop_grace_seconds = 4000\n"), code: CodeInvalidValue, path: "services.web.processes.a.stop_grace_seconds"},
		{name: "process stop grace zero", toml: t2doc(t2web + "[services.web.processes.a]\ncommand = [\"run\"]\nstop_grace_seconds = 0\n"), code: CodeInvalidValue, path: "services.web.processes.a.stop_grace_seconds"},
		{name: "process memory below minimum", toml: t2doc(t2web + "[services.web.processes.a]\ncommand = [\"run\"]\nmemory_mb = 3\n"), code: CodeInvalidValue, path: "services.web.processes.a.memory_mb"},
		{name: "process memory 0 inherits", toml: t2doc(t2web + "[services.web.processes.a]\ncommand = [\"run\"]\nmemory_mb = 0\n"), code: CodeInvalidValue, absent: true},
		{name: "process negative cpus", toml: t2doc(t2web + "[services.web.processes.a]\ncommand = [\"run\"]\ncpus = -1.0\n"), code: CodeInvalidValue, path: "services.web.processes.a.cpus"},
		{name: "process http health check needs the process port", toml: t2doc(t2web + "[services.web.processes.a]\ncommand = [\"run\"]\n\n[services.web.processes.a.health_check]\ntype = \"http\"\n"), code: CodeInvalidValue, path: "services.web.processes.a.health_check.type"},
		{name: "process health check uses the process port, not the service's", toml: t2doc(t2web + "[services.web.processes.a]\ncommand = [\"run\"]\nport = 81\n\n[services.web.processes.a.health_check]\ntype = \"http\"\n"), code: CodeInvalidValue, absent: true},
		{name: "process health check path", toml: t2doc(t2web + "[services.web.processes.a]\ncommand = [\"run\"]\nport = 81\n\n[services.web.processes.a.health_check]\npath = \"healthz\"\n"), code: CodeInvalidValue, path: "services.web.processes.a.health_check.path"},
		{name: "process health check exec command", toml: t2doc(t2web + "[services.web.processes.a]\ncommand = [\"run\"]\n\n[services.web.processes.a.health_check]\ntype = \"exec\"\ncommand = [\"true\"]\n"), code: CodeInvalidValue, path: "services.web.processes.a.health_check.command"},
		{name: "process scale replicas range", toml: t2doc(t2web + "[services.web.processes.a]\ncommand = [\"run\"]\n\n[services.web.processes.a.scale]\nreplicas = 11\n"), code: CodeInvalidValue, path: "services.web.processes.a.scale.replicas"},
		{name: "process autoscaling max below min", toml: t2doc(t2web + "[services.web.processes.a]\ncommand = [\"run\"]\n\n[services.web.processes.a.scale.autoscaling]\nmin = 4\nmax = 2\n"), code: CodeInvalidValue, path: "services.web.processes.a.scale.autoscaling.max"},
		{name: "singleton process with replicas", toml: t2doc(t2web + "[services.web.processes.clock]\ncommand = [\"run\"]\nsingleton = true\n\n[services.web.processes.clock.scale]\nreplicas = 2\n"), code: CodeProcessSingletonScale, path: "services.web.processes.clock.scale.replicas", line: "replicas = 2"},
		{name: "singleton process with autoscaling", toml: t2doc(t2web + "[services.web.processes.clock]\ncommand = [\"run\"]\nsingleton = true\n\n[services.web.processes.clock.scale.autoscaling]\nenabled = true\n"), code: CodeProcessSingletonScale, path: "services.web.processes.clock.scale.autoscaling.enabled"},
		{name: "singleton process with one replica", toml: t2doc(t2web + "[services.web.processes.clock]\ncommand = [\"run\"]\nsingleton = true\n\n[services.web.processes.clock.scale]\nreplicas = 1\n"), code: CodeProcessSingletonScale, absent: true},
		{name: "singleton process with autoscaling off", toml: t2doc(t2web + "[services.web.processes.clock]\ncommand = [\"run\"]\nsingleton = true\n\n[services.web.processes.clock.scale.autoscaling]\nenabled = false\n"), code: CodeProcessSingletonScale, absent: true},
		{name: "non-singleton process with replicas", toml: t2doc(t2web + "[services.web.processes.clock]\ncommand = [\"run\"]\nsingleton = false\n\n[services.web.processes.clock.scale]\nreplicas = 2\n"), code: CodeProcessSingletonScale, absent: true},
		{name: "singleton own process with replicas", toml: t2doc("[services.web]\nimage = \"nginx:1\"\nport = 80\nsingleton = true\n\n[services.web.scale]\nreplicas = 2\n"), code: CodeProcessSingletonScale, path: "services.web.scale.replicas"},
		{name: "singleton own process alone", toml: t2doc("[services.web]\nimage = \"nginx:1\"\nport = 80\nsingleton = true\n"), code: CodeProcessSingletonScale, absent: true},
		{name: "process of a service with volumes and replicas", toml: t2doc(t2web + "[services.web.volumes]\ndata = \"/data\"\n\n[services.web.processes.a]\ncommand = [\"run\"]\n\n[services.web.processes.a.scale]\nreplicas = 2\n"), code: CodeReplicasRequireStateles, path: "services.web.processes.a.scale.replicas"},
		{name: "process of a service with volumes and autoscaling", toml: t2doc(t2web + "[services.web.volumes]\ndata = \"/data\"\n\n[services.web.processes.a]\ncommand = [\"run\"]\n\n[services.web.processes.a.scale.autoscaling]\nenabled = true\nmax = 3\n"), code: CodeReplicasRequireStateles, path: "services.web.processes.a.scale.autoscaling.max"},
		{name: "process of a service with volumes, one replica", toml: t2doc(t2web + "[services.web.volumes]\ndata = \"/data\"\n\n[services.web.processes.a]\ncommand = [\"run\"]\n\n[services.web.processes.a.scale]\nreplicas = 1\n"), code: CodeReplicasRequireStateles, absent: true},
		{name: "processes on a one-shot service", toml: t2doc("[services.job]\nimage = \"busybox:1\"\none_shot = true\n\n[services.job.processes.a]\ncommand = [\"run\"]\n"), code: CodeInvalidValue, path: "services.job.processes"},
		{name: "processes on a git service", toml: t2doc("[services.web]\n\n[services.web.git]\nconnection = \"gh\"\nrepo = \"a/b\"\nbranch = \"main\"\n\n[services.web.processes.a]\ncommand = [\"run\"]\n"), code: CodeNotAllowedForKind, absent: true},
		{name: "domain routes to a process with a port", toml: t2doc(t2web + "[services.web.processes.a]\ncommand = [\"run\"]\nport = 81\n\n[services.web.domains.\"a.example.com\"]\nprocess = \"a\"\n"), code: CodeInvalidValue, absent: true},
		{name: "domain names an unknown process", toml: t2doc(t2web + "[services.web.processes.a]\ncommand = [\"run\"]\nport = 81\n\n[services.web.domains.\"a.example.com\"]\nprocess = \"ghost\"\n"), code: CodeInvalidValue, path: "services.web.domains.\"a.example.com\".process", line: "process = \"ghost\""},
		{name: "domain names a process without a port", toml: t2doc(t2web + "[services.web.processes.a]\ncommand = [\"run\"]\n\n[services.web.domains.\"a.example.com\"]\nprocess = \"a\"\n"), code: CodeInvalidValue, path: "services.web.domains.\"a.example.com\".process"},
		{name: "domain names the own process", toml: t2doc(t2web + "[services.web.processes.a]\ncommand = [\"run\"]\nport = 81\n\n[services.web.domains.\"a.example.com\"]\nprocess = \"web\"\n"), code: CodeInvalidValue, absent: true},
		{name: "domain without process uses the own process", toml: t2doc(t2web + "[services.web.processes.a]\ncommand = [\"run\"]\nport = 81\n\n[services.web.domains.\"a.example.com\"]\n"), code: CodeInvalidValue, absent: true},
		{name: "domain without process when several processes have a port", toml: t2doc("[services.web]\nimage = \"nginx:1\"\n\n[services.web.processes.a]\ncommand = [\"run\"]\nport = 81\n\n[services.web.processes.b]\ncommand = [\"run\"]\nport = 82\n\n[services.web.domains.\"a.example.com\"]\n"), code: CodeInvalidValue, path: "services.web.domains.\"a.example.com\""},
		{name: "domain without process picks the only process with a port", toml: t2doc("[services.web]\nimage = \"nginx:1\"\n\n[services.web.processes.a]\ncommand = [\"run\"]\nport = 81\n\n[services.web.domains.\"a.example.com\"]\n"), code: CodeInvalidValue, absent: true},
		{name: "domain target port must be the process port", toml: t2doc(t2web + "[services.web.processes.a]\ncommand = [\"run\"]\nport = 81\n\n[services.web.domains.\"a.example.com\"]\nprocess = \"a\"\ntarget_port = 82\n"), code: CodeInvalidValue, path: "services.web.domains.\"a.example.com\".target_port"},
		{name: "domain process on a service without processes", toml: t2doc(t2web + "[services.web.domains.\"a.example.com\"]\nprocess = \"web\"\n"), code: CodeInvalidValue, path: "services.web.domains.\"a.example.com\".process"},
		{name: "domain process on a stack", toml: t2doc("[services.s]\n\n[services.s.stack]\ncompose = '''\nservices:\n  web:\n    image: nginx:1\n'''\n\n[services.s.domains.\"a.example.com\"]\ncomponent = \"web\"\nprocess = \"web\"\n"), code: CodeInvalidValue, path: "services.s.domains.\"a.example.com\".process"},
		{name: "backup every minute", toml: t2doc("[services.db]\n\n[services.db.database]\nengine = \"postgres\"\n\n[services.db.backups.nightly]\ndestination = \"d\"\ncron = \"* * * * *\"\n"), code: CodeInvalidValue, path: "services.db.backups.nightly.cron", line: "cron ="},
		{name: "backup every two minutes", toml: t2doc("[services.db]\n\n[services.db.database]\nengine = \"postgres\"\n\n[services.db.backups.nightly]\ndestination = \"d\"\ncron = \"*/2 * * * *\"\n"), code: CodeInvalidValue, path: "services.db.backups.nightly.cron"},
		{name: "backup every five minutes", toml: t2doc("[services.db]\n\n[services.db.database]\nengine = \"postgres\"\n\n[services.db.backups.nightly]\ndestination = \"d\"\ncron = \"*/5 * * * *\"\n"), code: CodeInvalidValue, absent: true},
		{name: "backup hourly alias", toml: t2doc("[services.db]\n\n[services.db.database]\nengine = \"postgres\"\n\n[services.db.backups.nightly]\ndestination = \"d\"\ncron = \"@hourly\"\n"), code: CodeInvalidValue, absent: true},
		{name: "backup timezone invalid", toml: t2doc("[services.db]\n\n[services.db.database]\nengine = \"postgres\"\n\n[services.db.backups.nightly]\ndestination = \"d\"\ncron = \"0 2 * * *\"\ntimezone = \"Nowhere/City\"\n"), code: CodeInvalidValue, path: "services.db.backups.nightly.timezone"},
		{name: "backup without destination", toml: t2doc("[services.db]\n\n[services.db.database]\nengine = \"postgres\"\n\n[services.db.backups.nightly]\ncron = \"0 2 * * *\"\n"), code: CodeMissingRequired, path: "services.db.backups.nightly.destination"},
		{name: "backup without cron", toml: t2doc("[services.db]\n\n[services.db.database]\nengine = \"postgres\"\n\n[services.db.backups.nightly]\ndestination = \"d\"\n"), code: CodeMissingRequired, path: "services.db.backups.nightly.cron"},
		{name: "backup retention too large", toml: t2doc("[services.db]\n\n[services.db.database]\nengine = \"postgres\"\n\n[services.db.backups.nightly]\ndestination = \"d\"\ncron = \"0 2 * * *\"\nretention_count = 1001\n"), code: CodeInvalidValue, path: "services.db.backups.nightly.retention_count"},
		{name: "backup key upper-case", toml: t2doc("[services.db]\n\n[services.db.database]\nengine = \"postgres\"\n\n[services.db.backups.Nightly]\ndestination = \"d\"\ncron = \"0 2 * * *\"\n"), code: CodeInvalidValue, path: "services.db.backups.Nightly"},

		// domains
		{name: "domain upper-case host", toml: t2doc(t2web + "[services.web.domains.\"Shop.Example.com\"]\n"), code: CodeInvalidValue, path: "services.web.domains.\"Shop.Example.com\""},
		{name: "domain invalid host", toml: t2doc(t2web + "[services.web.domains.\"not a host\"]\n"), code: CodeInvalidValue, path: "services.web.domains.\"not a host\""},
		{name: "domain with path prefix", toml: t2doc(t2web + "[services.web.domains.\"shop.example.com/api\"]\nstrip_prefix = true\n"), code: CodeInvalidValue, absent: true},
		{name: "domain with bad path prefix", toml: t2doc(t2web + "[services.web.domains.\"shop.example.com/api/\"]\n"), code: CodeInvalidValue, path: "services.web.domains.\"shop.example.com/api/\""},
		{name: "domain wildcard host", toml: t2doc(t2web + "[services.web.domains.\"*.example.com\"]\n"), code: CodeInvalidValue, absent: true},
		{name: "domain on a worker", toml: t2doc("[services.w]\nimage = \"nginx:1\"\n\n[services.w.domains.\"w.example.com\"]\n"), code: CodeMissingRequired, path: "services.w.domains.\"w.example.com\""},
		{name: "domain on a worker with target_port", toml: t2doc("[services.w]\nimage = \"nginx:1\"\n\n[services.w.domains.\"w.example.com\"]\ntarget_port = 8080\n"), code: CodeMissingRequired, absent: true},
		{name: "redirect without target", toml: t2doc(t2web + "[services.web.domains.\"old.example.com\"]\nkind = \"redirect\"\n"), code: CodeMissingRequired, path: "services.web.domains.\"old.example.com\".redirect_to"},
		{name: "redirect with target", toml: t2doc(t2web + "[services.web.domains.\"old.example.com\"]\nkind = \"redirect\"\nredirect_to = \"new.example.com\"\nredirect_code = 301\n"), code: CodeMissingRequired, absent: true},
		{name: "redirect code not allowed", toml: t2doc(t2web + "[services.web.domains.\"old.example.com\"]\nkind = \"redirect\"\nredirect_to = \"new.example.com\"\nredirect_code = 303\n"), code: CodeInvalidValue, path: "services.web.domains.\"old.example.com\".redirect_code"},
		{name: "certificate without custom tls", toml: t2doc(t2web + "[services.web.domains.\"a.example.com\"]\ncertificate = \"wildcard\"\n"), code: CodeInvalidValue, path: "services.web.domains.\"a.example.com\".certificate"},
		{name: "certificate with custom tls", toml: t2doc(t2web + "[services.web.domains.\"a.example.com\"]\ntls = \"custom\"\ncertificate = \"wildcard\"\n"), code: CodeInvalidValue, absent: true},
		{name: "options security headers", toml: t2doc(t2web + "[services.web.domains.\"a.example.com\".options]\nsecurity_headers = \"loose\"\n"), code: CodeInvalidValue, path: "services.web.domains.\"a.example.com\".options.security_headers"},
		{name: "options access cidr", toml: t2doc(t2web + "[services.web.domains.\"a.example.com\".options.access]\nallow_cidrs = [\"not-a-cidr\"]\n"), code: CodeInvalidValue, path: "services.web.domains.\"a.example.com\".options.access"},
		{name: "options access valid cidr", toml: t2doc(t2web + "[services.web.domains.\"a.example.com\".options.access]\nallow_cidrs = [\"203.0.113.0/24\"]\n"), code: CodeInvalidValue, absent: true},
		{name: "options cors without origins", toml: t2doc(t2web + "[services.web.domains.\"a.example.com\".options.cors]\nmethods = [\"GET\"]\n"), code: CodeMissingRequired, path: "services.web.domains.\"a.example.com\".options.cors.origins"},
		{name: "options rate limit incomplete", toml: t2doc(t2web + "[services.web.domains.\"a.example.com\".options.rate_limit]\naverage = 10\n"), code: CodeMissingRequired, path: "services.web.domains.\"a.example.com\".options.rate_limit"},
		{name: "domain on a tcp route", toml: t2doc("[services.r]\n\n[services.r.route]\nprotocol = \"tcp\"\nlisten_port = 25565\nopen_to_internet = true\nupstreams = [{ address = \"10.0.0.1:25565\" }]\n\n[services.r.domains.\"r.example.com\"]\n"), code: CodeInvalidValue, path: "services.r.domains.\"r.example.com\""},
		{name: "domain on an http route", toml: t2doc("[services.r]\n\n[services.r.route]\nprotocol = \"http\"\nupstreams = [{ address = \"10.0.0.1:80\" }]\n\n[services.r.domains.\"r.example.com\"]\n"), code: CodeInvalidValue, absent: true},
		{name: "component outside a stack", toml: t2doc(t2web + "[services.web.domains.\"a.example.com\"]\ncomponent = \"web\"\n"), code: CodeInvalidValue, path: "services.web.domains.\"a.example.com\".component"},
		{name: "generate_domain on a worker", toml: t2doc("[services.w]\nimage = \"nginx:1\"\ngenerate_domain = true\n"), code: CodeInvalidValue, path: "services.w.generate_domain", severity: SeverityWarning},
		{name: "generate_domain with a port", toml: t2doc("[services.w]\nimage = \"nginx:1\"\nport = 80\ngenerate_domain = true\n"), code: CodeInvalidValue, absent: true},
		{name: "generate_domain with the only port on a named process", toml: t2doc("[services.w]\nimage = \"nginx:1\"\ngenerate_domain = true\n\n[services.w.processes.a]\ncommand = [\"run\"]\nport = 81\n"), code: CodeInvalidValue, absent: true},
		{name: "generate_domain with no port on any process", toml: t2doc("[services.w]\nimage = \"nginx:1\"\ngenerate_domain = true\n\n[services.w.processes.a]\ncommand = [\"run\"]\n"), code: CodeInvalidValue, path: "services.w.generate_domain", severity: SeverityWarning},
		{name: "generate_domain with two named processes with a port", toml: t2doc("[services.w]\nimage = \"nginx:1\"\ngenerate_domain = true\n\n[services.w.processes.a]\ncommand = [\"run\"]\nport = 81\n\n[services.w.processes.b]\ncommand = [\"run\"]\nport = 82\n"), code: CodeInvalidValue, path: "services.w.generate_domain", severity: SeverityError},
		{name: "generate_domain with two ports but a default port", toml: t2doc("[services.w]\nimage = \"nginx:1\"\nport = 80\ngenerate_domain = true\n\n[services.w.processes.a]\ncommand = [\"run\"]\nport = 81\n\n[services.w.processes.b]\ncommand = [\"run\"]\nport = 82\n"), code: CodeInvalidValue, absent: true},

		{name: "hostname_in_use across two services", toml: t2doc("[services.a]\nimage = \"nginx:1\"\nport = 80\n\n[services.a.domains.\"x.example.com\"]\n\n[services.b]\nimage = \"nginx:1\"\nport = 80\n\n[services.b.domains.\"x.example.com\"]\n"), code: CodeHostnameInUse, path: "services.b.domains.\"x.example.com\""},
		{name: "hostname_in_use different hosts", toml: t2doc("[services.a]\nimage = \"nginx:1\"\nport = 80\n\n[services.a.domains.\"x.example.com\"]\n\n[services.b]\nimage = \"nginx:1\"\nport = 80\n\n[services.b.domains.\"y.example.com\"]\n"), code: CodeHostnameInUse, absent: true},
		{name: "hostname_in_use different path prefixes", toml: t2doc("[services.a]\nimage = \"nginx:1\"\nport = 80\n\n[services.a.domains.\"x.example.com\"]\n\n[services.b]\nimage = \"nginx:1\"\nport = 80\n\n[services.b.domains.\"x.example.com/api\"]\n"), code: CodeHostnameInUse, absent: true},
		{name: "hostname_in_use same host and prefix", toml: t2doc("[services.a]\nimage = \"nginx:1\"\nport = 80\n\n[services.a.domains.\"x.example.com/api\"]\n\n[services.b]\nimage = \"nginx:1\"\nport = 80\n\n[services.b.domains.\"x.example.com/api\"]\n"), code: CodeHostnameInUse, path: "services.b.domains.\"x.example.com/api\""},
		{name: "hostname_in_use only in one environment", toml: t2doc("[services.a]\nimage = \"nginx:1\"\nport = 80\n\n[services.a.domains.\"x.example.com\"]\n\n[services.b]\nimage = \"nginx:1\"\nport = 80\n\n[environments.staging.services.b.domains.\"x.example.com\"]\n"), code: CodeHostnameInUse, path: "environments.staging.services.b.domains.\"x.example.com\"", env: "staging"},

		// files
		{name: "file path relative", toml: t2doc(t2web + "[services.web.files.\"etc/a.conf\"]\ncontent = \"x\"\n"), code: CodeInvalidValue, path: "services.web.files.\"etc/a.conf\""},
		{name: "file mode not octal", toml: t2doc(t2web + "[services.web.files.\"/etc/a.conf\"]\nmode = \"rw\"\ncontent = \"x\"\n"), code: CodeInvalidValue, path: "services.web.files.\"/etc/a.conf\".mode"},
		{name: "file content and source", toml: t2doc(t2web + "[services.web.files.\"/etc/a.conf\"]\ncontent = \"x\"\nsource = \"a.conf\"\n"), code: CodeInvalidValue, path: "services.web.files.\"/etc/a.conf\".source"},
		{name: "file without content or source", toml: t2doc(t2web + "[services.web.files.\"/etc/a.conf\"]\nmode = \"0600\"\n"), code: CodeMissingRequired, path: "services.web.files.\"/etc/a.conf\""},
		{name: "file valid", toml: t2doc(t2web + "[services.web.files.\"/etc/a.conf\"]\nmode = \"0600\"\ncontent = \"listen = 80\\n\"\n"), code: CodeInvalidValue, absent: true},
	}
}

func t2LimitCases() []t2case {
	vars := func(n int) string { return t2lines(n, "V%03d = \"x\"\n") }
	services := func(n int) string { return t2lines(n, "[services.s%02d]\nimage = \"nginx:1\"\n\n") }
	domains := func(n int) string {
		return t2lines(n, "[services.web.domains.\"d%02d.example.com\"]\n\n")
	}
	files := func(n int) string {
		return t2lines(n, "[services.web.files.\"/etc/f%02d.conf\"]\ncontent = \"x\"\n\n")
	}
	processes := func(n int) string {
		return t2lines(n, "[services.web.processes.p%02d]\ncommand = [\"run\"]\n\n")
	}
	upstreams := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = fmt.Sprintf("{ address = \"10.0.0.%d:80\" }", i+1)
		}
		return "[services.r]\n\n[services.r.route]\nprotocol = \"http\"\nupstreams = [" + strings.Join(parts, ", ") + "]\n"
	}
	unset := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = fmt.Sprintf("\"variables.V%d\"", i)
		}
		return t2web + "[environments.staging.services.web]\nunset = [" + strings.Join(parts, ", ") + "]\n"
	}
	envs := func(n int) string { return t2lines(n, "[environments.e%02d]\nserver = \"x\"\n\n") }
	return []t2case{
		{name: "shared variables 501", toml: "schema = 1\n\n[variables]\n" + vars(501), code: CodeLimitExceeded, path: "variables"},
		{name: "shared variables 500", toml: "schema = 1\n\n[variables]\n" + vars(500), code: CodeLimitExceeded, absent: true},
		{name: "service variables 501", toml: t2doc(t2web + "[services.web.variables]\n" + vars(501)), code: CodeLimitExceeded, path: "services.web.variables"},
		{name: "service variables 500", toml: t2doc(t2web + "[services.web.variables]\n" + vars(500)), code: CodeLimitExceeded, absent: true},
		{name: "services 65", toml: t2doc(services(65)), code: CodeLimitExceeded, path: "services"},
		{name: "services 64", toml: t2doc(services(64)), code: CodeLimitExceeded, absent: true},
		{name: "domains 51", toml: t2doc(t2web + domains(51)), code: CodeLimitExceeded, path: "services.web.domains"},
		{name: "domains 50", toml: t2doc(t2web + domains(50)), code: CodeLimitExceeded, absent: true},
		{name: "files 17", toml: t2doc(t2web + files(17)), code: CodeLimitExceeded, path: "services.web.files"},
		{name: "files 16", toml: t2doc(t2web + files(16)), code: CodeLimitExceeded, absent: true},
		{name: "tasks 21", toml: t2doc(t2web + t2tasks(21)), code: CodeLimitExceeded, path: "services.web.tasks"},
		{name: "tasks 20", toml: t2doc(t2web + t2tasks(20)), code: CodeLimitExceeded, absent: true},
		{name: "processes 10", toml: t2doc(t2web + processes(10)), code: CodeLimitExceeded, path: "services.web.processes"},
		{name: "processes 9", toml: t2doc(t2web + processes(9)), code: CodeLimitExceeded, absent: true},
		{name: "upstreams 21", toml: t2doc(upstreams(21)), code: CodeLimitExceeded, path: "services.r.route.upstreams"},
		{name: "upstreams 20", toml: t2doc(upstreams(20)), code: CodeLimitExceeded, absent: true},
		{name: "unset paths 201", toml: t2doc(unset(201)), code: CodeLimitExceeded, path: "environments.staging.services.web.unset"},
		{name: "unset paths 200", toml: t2doc(unset(200)), code: CodeLimitExceeded, absent: true},
		{name: "environments 33", toml: t2doc(t2web + envs(33)), code: CodeLimitExceeded, path: "environments"},
		{name: "environments 32", toml: t2doc(t2web + envs(32)), code: CodeLimitExceeded, absent: true},
		{name: "string over 64 KiB", toml: t2doc(t2web + "[services.web.variables]\nBIG = \"" + strings.Repeat("a", MaxStringBytes+1) + "\"\n"), code: CodeLimitExceeded, path: "services.web.variables.BIG"},
		{name: "string at 64 KiB", toml: t2doc(t2web + "[services.web.variables]\nBIG = \"" + strings.Repeat("a", MaxStringBytes) + "\"\n"), code: CodeLimitExceeded, absent: true},
		{name: "tables nested 12 deep", toml: "schema = 1\n\n[a.b.c.d.e.f.g.h.i.j.k.l]\nx = 1\n", code: CodeLimitExceeded},
		{name: "tables nested 10 deep", toml: "schema = 1\n\n[a.b.c.d.e.f.g.h.i.j]\nx = 1\n", code: CodeLimitExceeded, absent: true},
		{name: "one file over 64 KiB", toml: t2doc(t2web + "[services.web.files.\"/etc/big\"]\ncontent = \"" + strings.Repeat("a", MaxFileContentBytes+1) + "\"\n"), code: CodeLimitExceeded, path: "services.web.files.\"/etc/big\""},
	}
}
