package main

import (
	"bytes"
	"fmt"
	"html/template"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"
)

const outFile = "health_check.html"

type SystemInfo struct {
	Hostname        string
	OS              string
	Platform        string
	PlatformVersion string
	KernelVersion   string
	Architecture    string
	UptimeSec       uint64
	UptimeDays      float64
}

type CPUInfo struct {
	LogicalCPUs  int
	PhysicalCPUs int
	Model        string
	MHz          float64
	Cores        int32
	UsageOverall []float64
	UsagePerCPU  []float64
	UserTime     float64
	SystemTime   float64
	IdleTime     float64
	IOWait       float64
	Load1        float64
	Load5        float64
	Load15       float64
}

type MemInfo struct {
	Total       string
	Available   string
	Used        string
	UsedPercent float64
	Free        string
	Cached      string
	Buffers     string
	Active      string
	Inactive    string
	Shared      string

	SwapTotal       string
	SwapUsed        string
	SwapFree        string
	SwapUsedPercent float64
	SwapSin         string
	SwapSout        string
}

type DiskUsageRow struct {
	Mountpoint string
	FSType     string
	Device     string
	Total      string
	Used       string
	Free       string
	UsePct     float64
	Warn       bool
}
type DiskIOStat struct {
	Name        string
	ReadCount   uint64
	WriteCount  uint64
	ReadBytes   string
	WriteBytes  string
	ReadTimeMS  uint64
	WriteTimeMS uint64
}
type DiskInfo struct {
	Rows     []DiskUsageRow
	Warnings int
	IOStats  []DiskIOStat
}

type ErrSummary struct {
	Total   int
	Permanent int
	Temporary int
	Informational int
	Unknown int
}

type ErrorLog struct {
	AllTimeSummary   ErrSummary
	Last24hSummary   ErrSummary
	Last24hFullBlock string // concatenated full errpt entries in last 24h
	Note             string
}

type Report struct {
	GeneratedAt time.Time
	System      SystemInfo
	CPU         CPUInfo
	Memory      MemInfo
	Disks       DiskInfo
	Errors      ErrorLog
}

func main() {
	rep := Report{
		GeneratedAt: time.Now(),
		System:      getSystemInfo(),
	}
	rep.CPU = getCPUInfo()
	rep.Memory = getMemoryInfo()
	rep.Disks = getDiskInfo()
	rep.Errors = getErrors()

	if err := writeHTML(rep, outFile); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write HTML: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ Health check written to %s\n", outFile)
}

func getSystemInfo() SystemInfo {
	info, err := host.Info()
	if err != nil {
		return SystemInfo{Hostname: fmt.Sprintf("error: %v", err)}
	}
	return SystemInfo{
		Hostname:        info.Hostname,
		OS:              info.OS,
		Platform:        info.Platform,
		PlatformVersion: info.PlatformVersion,
		KernelVersion:   info.KernelVersion,
		Architecture:    info.KernelArch,
		UptimeSec:       info.Uptime,
		UptimeDays:      float64(info.Uptime) / 86400.0,
	}
}

func getCPUInfo() CPUInfo {
	var out CPUInfo

	if v, err := cpu.Counts(true); err == nil {
		out.LogicalCPUs = v
	}
	if v, err := cpu.Counts(false); err == nil {
		out.PhysicalCPUs = v
	}
	if infos, err := cpu.Info(); err == nil && len(infos) > 0 {
		out.Model = infos[0].ModelName
		out.MHz = infos[0].Mhz
		out.Cores = infos[0].Cores
	}
	if pct, err := cpu.Percent(time.Second, false); err == nil {
		out.UsageOverall = pct
	}
	if per, err := cpu.Percent(time.Second, true); err == nil {
		out.UsagePerCPU = per
	}
	if t, err := cpu.Times(false); err == nil && len(t) > 0 {
		out.UserTime = t[0].User
		out.SystemTime = t[0].System
		out.IdleTime = t[0].Idle
		out.IOWait = t[0].Iowait
	}
	if la, err := load.Avg(); err == nil {
		out.Load1, out.Load5, out.Load15 = la.Load1, la.Load5, la.Load15
	}
	return out
}

func getMemoryInfo() MemInfo {
	var m MemInfo
	if vm, err := mem.VirtualMemory(); err == nil {
		// Some platforms (incl. AIX in some builds) may report Available=0.
		available := vm.Available
		if available == 0 {
			available = vm.Free + vm.Cached + vm.Buffers
		}
		m.Total = formatBytes(vm.Total)
		m.Available = formatBytes(available)
		m.Used = formatBytes(vm.Used)
		m.UsedPercent = vm.UsedPercent
		m.Free = formatBytes(vm.Free)
		m.Cached = formatBytes(vm.Cached)
		m.Buffers = formatBytes(vm.Buffers)
		if vm.Active > 0 {
			m.Active = formatBytes(vm.Active)
		}
		if vm.Inactive > 0 {
			m.Inactive = formatBytes(vm.Inactive)
		}
		if vm.Shared > 0 {
			m.Shared = formatBytes(vm.Shared)
		}
	}
	if sw, err := mem.SwapMemory(); err == nil {
		m.SwapTotal = formatBytes(sw.Total)
		m.SwapUsed = formatBytes(sw.Used)
		m.SwapFree = formatBytes(sw.Free)
		m.SwapUsedPercent = sw.UsedPercent
		m.SwapSin = formatBytes(sw.Sin)
		m.SwapSout = formatBytes(sw.Sout)
	}
	return m
}

func getDiskInfo() DiskInfo {
	var di DiskInfo

	parts, err := disk.Partitions(false)
	if err != nil {
		return di
	}

	// sort by mountpoint for readability
	sort.Slice(parts, func(i, j int) bool { return parts[i].Mountpoint < parts[j].Mountpoint })

	for _, p := range parts {
		u, err := disk.Usage(p.Mountpoint)
		if err != nil {
			di.Rows = append(di.Rows, DiskUsageRow{
				Mountpoint: p.Mountpoint,
				FSType:     p.Fstype,
				Device:     p.Device,
				Total:      "error",
				Used:       "error",
				Free:       "error",
				UsePct:     0,
				Warn:       false,
			})
			continue
		}
		row := DiskUsageRow{
			Mountpoint: p.Mountpoint,
			FSType:     p.Fstype,
			Device:     p.Device,
			Total:      formatBytes(u.Total),
			Used:       formatBytes(u.Used),
			Free:       formatBytes(u.Free),
			UsePct:     u.UsedPercent,
			Warn:       u.UsedPercent > 80.0,
		}
		if row.Warn {
			di.Warnings++
		}
		di.Rows = append(di.Rows, row)
	}

	if ioc, err := disk.IOCounters(); err == nil {
		names := make([]string, 0, len(ioc))
		for k := range ioc {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, name := range names {
			c := ioc[name]
			di.IOStats = append(di.IOStats, DiskIOStat{
				Name:        name,
				ReadCount:   c.ReadCount,
				WriteCount:  c.WriteCount,
				ReadBytes:   formatBytes(c.ReadBytes),
				WriteBytes:  formatBytes(c.WriteBytes),
				ReadTimeMS:  c.ReadTime,
				WriteTimeMS: c.WriteTime,
			})
		}
	}

	return di
}

// getErrors builds both all-time and last-24h summaries and collects all full entries in last 24h.
func getErrors() ErrorLog {
	var el ErrorLog

	// all-time (hardware-focused like before)
	allOut, err := exec.Command("errpt", "-a", "-d", "H").Output()
	if err != nil {
		el.Note = "Error invoking errpt (AIX): " + err.Error()
		return el
	}
	allEntries := splitErrptEntries(string(allOut))
	el.AllTimeSummary = summarizeEntries(allEntries)

	// last 24h filter by parsing Date/Time inside entries
	cut := time.Now().Add(-24 * time.Hour)
	var last24 []string
	for _, e := range allEntries {
		ts, ok := extractErrptTime(e)
		if !ok {
			continue
		}
		if ts.After(cut) {
			last24 = append(last24, e)
		}
	}
	el.Last24hSummary = summarizeEntries(last24)
	el.Last24hFullBlock = strings.Join(last24, "\n\n")

	return el
}

// splitErrptEntries splits full `errpt -a` output into discrete entries (each begins with a dashed line OR LABEL:)
func splitErrptEntries(s string) []string {
	lines := strings.Split(s, "\n")
	var out []string
	var cur []string
	flush := func() {
		if len(cur) == 0 {
			return
		}
		entry := strings.TrimSpace(strings.Join(cur, "\n"))
		if entry != "" {
			out = append(out, entry)
		}
		cur = nil
	}

	for _, ln := range lines {
		trim := strings.TrimSpace(ln)
		// AIX typically draws a dashed separator line. Also accept "LABEL:" as a start marker.
		if strings.HasPrefix(trim, "--------------------------------") || strings.HasPrefix(trim, "LABEL:") && len(cur) > 0 {
			flush()
		}
		cur = append(cur, ln)
	}
	flush()
	return out
}

func extractErrptTime(entry string) (time.Time, bool) {
	// Example: "Date/Time:       Fri Oct  3 12:12:21 AST 2025"
	const layout = "Mon Jan 2 15:04:05 MST 2006"
	for _, ln := range strings.Split(entry, "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "Date/Time:") {
			parts := strings.SplitN(ln, "Date/Time:", 2)
			if len(parts) != 2 {
				continue
			}
			raw := strings.TrimSpace(parts[1])
			// Normalize double spaces before day-of-month to single space for parsing "Jan  3" -> "Jan 3"
			raw = strings.ReplaceAll(raw, "  ", " ")
			if t, err := time.Parse(layout, raw); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

func summarizeEntries(entries []string) ErrSummary {
	var s ErrSummary
	for _, e := range entries {
		s.Total++
		t := extractType(e)
		switch t {
		case "PERM":
			s.Permanent++
		case "TEMP":
			s.Temporary++
		case "INFO":
			s.Informational++
		default:
			s.Unknown++
		}
	}
	return s
}

func extractType(entry string) string {
	for _, ln := range strings.Split(entry, "\n") {
		trim := strings.TrimSpace(ln)
		if strings.HasPrefix(trim, "Type:") {
			v := strings.TrimSpace(strings.TrimPrefix(trim, "Type:"))
			// Common values: PERM, TEMP, INFO, UNKN
			return strings.ToUpper(v)
		}
	}
	return ""
}

func writeHTML(rep Report, path string) error {
	tpl := template.Must(template.New("page").Funcs(template.FuncMap{
		"pct": func(f float64) string { return fmt.Sprintf("%.2f%%", f) },
	}).Parse(htmlTemplate))

	var buf bytes.Buffer
	if err := tpl.Execute(&buf, rep); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0644)
}

func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

const htmlTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>AIX Power9 System Health Check</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
:root{--bg:#0b1320;--card:#121a2b;--muted:#9aa4b2;--text:#e6edf3;--ok:#12b886;--warn:#f59f00;--bad:#e03131;--b:#22304a;--tbl:#162139}
html,body{background:var(--bg);color:var(--text);font-family:system-ui,-apple-system,Segoe UI,Roboto,Ubuntu,Cantarell,'Noto Sans',sans-serif;margin:0;padding:0}
.wrap{max-width:1100px;margin:32px auto;padding:0 16px}
.hdr{display:flex;justify-content:space-between;align-items:center;margin-bottom:16px}
.h1{font-size:24px;font-weight:700}
.time{color:var(--muted);font-size:14px}
.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(280px,1fr));gap:16px}
.card{background:var(--card);border:1px solid var(--b);border-radius:12px;padding:16px}
.card h2{margin:.2rem 0 .8rem;font-size:16px}
.kv{display:grid;grid-template-columns:1fr 1fr;gap:6px 12px;font-size:14px}
.kv div{padding:2px 0;border-bottom:1px dashed rgba(255,255,255,.06)}
.tbl{width:100%;border-collapse:collapse;font-size:14px;background:var(--tbl);border:1px solid var(--b);overflow:hidden;border-radius:8px}
.tbl th,.tbl td{padding:8px 10px;border-bottom:1px solid var(--b);text-align:left;vertical-align:top}
.tbl th{font-weight:600;color:#c8d0da;background:#0f1a2f}
.badge{display:inline-block;padding:2px 8px;border-radius:999px;font-size:12px}
.badge.ok{background:rgba(18,184,134,.15);color:var(--ok);border:1px solid rgba(18,184,134,.4)}
.badge.warn{background:rgba(245,159,0,.15);color:var(--warn);border:1px solid rgba(245,159,0,.4)}
pre{white-space:pre-wrap;word-wrap:break-word;background:#0f1a2f;border:1px solid var(--b);padding:12px;border-radius:8px;margin:0}
.small{color:var(--muted);font-size:12px}
.footer{margin-top:20px;color:var(--muted);font-size:12px;text-align:center}
</style>
</head>
<body>
<div class="wrap">
  <div class="hdr">
    <div class="h1">AIX Power9 System Health Check</div>
    <div class="time">{{ .GeneratedAt.Format "2006-01-02 15:04:05 MST" }}</div>
  </div>

  <div class="grid">
    <section class="card">
      <h2>System Information</h2>
      <div class="kv">
        <div>Hostname</div><div>{{ .System.Hostname }}</div>
        <div>OS</div><div>{{ .System.OS }}</div>
        <div>Platform</div><div>{{ .System.Platform }}</div>
        <div>Platform Version</div><div>{{ .System.PlatformVersion }}</div>
        <div>Kernel</div><div>{{ .System.KernelVersion }}</div>
        <div>Arch</div><div>{{ .System.Architecture }}</div>
        <div>Uptime</div><div>{{ .System.UptimeSec }}s ({{ printf "%.2f" .System.UptimeDays }} days)</div>
      </div>
    </section>

    <section class="card">
      <h2>CPU</h2>
      <div class="kv">
        <div>Logical CPUs</div><div>{{ .CPU.LogicalCPUs }}</div>
        <div>Physical CPUs</div><div>{{ .CPU.PhysicalCPUs }}</div>
        <div>Model</div><div>{{ .CPU.Model }}</div>
        <div>MHz</div><div>{{ printf "%.2f" .CPU.MHz }}</div>
        <div>Cores (per socket)</div><div>{{ .CPU.Cores }}</div>
        {{ if .CPU.UsageOverall }}
          <div>Usage (overall)</div><div>{{ printf "%.2f%%" (index .CPU.UsageOverall 0) }}</div>
        {{ end }}
        <div>Load Average</div><div>1m {{ printf "%.2f" .CPU.Load1 }}, 5m {{ printf "%.2f" .CPU.Load5 }}, 15m {{ printf "%.2f" .CPU.Load15 }}</div>
        <div>User/System/Idle/IOwait (s)</div>
        <div>{{ printf "%.2f / %.2f / %.2f / %.2f" .CPU.UserTime .CPU.SystemTime .CPU.IdleTime .CPU.IOWait }}</div>
      </div>
      {{ if .CPU.UsagePerCPU }}
        <div class="small" style="margin-top:8px;">Per-CPU usage:</div>
        <table class="tbl" style="margin-top:6px;">
          <thead><tr><th>CPU</th><th>Usage</th></tr></thead>
          <tbody>
          {{ range $i, $v := .CPU.UsagePerCPU }}
            <tr><td>CPU {{ $i }}</td><td>{{ printf "%.2f%%" $v }}</td></tr>
          {{ end }}
          </tbody>
        </table>
      {{ end }}
    </section>

    <section class="card">
      <h2>Memory</h2>
      <div class="kv">
        <div>Total</div><div>{{ .Memory.Total }}</div>
        <div>Available</div><div>{{ .Memory.Available }}</div>
        <div>Used</div><div>{{ .Memory.Used }}</div>
        <div>Used %</div><div>{{ pct .Memory.UsedPercent }}</div>
        <div>Free</div><div>{{ .Memory.Free }}</div>
        <div>Cached</div><div>{{ .Memory.Cached }}</div>
        <div>Buffers</div><div>{{ .Memory.Buffers }}</div>
        {{ if .Memory.Active }}<div>Active</div><div>{{ .Memory.Active }}</div>{{ end }}
        {{ if .Memory.Inactive }}<div>Inactive</div><div>{{ .Memory.Inactive }}</div>{{ end }}
        {{ if .Memory.Shared }}<div>Shared</div><div>{{ .Memory.Shared }}</div>{{ end }}
        <div>Swap Total</div><div>{{ .Memory.SwapTotal }}</div>
        <div>Swap Used</div><div>{{ .Memory.SwapUsed }} ({{ pct .Memory.SwapUsedPercent }})</div>
        <div>Swap Free</div><div>{{ .Memory.SwapFree }}</div>
        <div>Swap In/Out</div><div>{{ .Memory.SwapSin }} / {{ .Memory.SwapSout }}</div>
      </div>
    </section>
  </div>

  <section class="card" style="margin-top:16px;">
    <h2>Disk Usage</h2>
    <table class="tbl">
      <thead>
        <tr><th>Mountpoint</th><th>FS Type</th><th>Device</th><th>Total</th><th>Used</th><th>Free</th><th>Use%</th><th>Status</th></tr>
      </thead>
      <tbody>
      {{ range .Disks.Rows }}
        <tr>
          <td>{{ .Mountpoint }}</td>
          <td>{{ .FSType }}</td>
          <td>{{ .Device }}</td>
          <td>{{ .Total }}</td>
          <td>{{ .Used }}</td>
          <td>{{ .Free }}</td>
          <td>{{ printf "%.2f%%" .UsePct }}</td>
          <td>{{ if .Warn }}<span class="badge warn">> 80%</span>{{ else }}<span class="badge ok">OK</span>{{ end }}</td>
        </tr>
      {{ end }}
      </tbody>
    </table>
    <div style="margin-top:8px;">
      {{ if gt .Disks.Warnings 0 }}
        <span class="badge warn">{{ .Disks.Warnings }} filesystem(s) > 80%</span>
      {{ else }}
        <span class="badge ok">All filesystems below 80%</span>
      {{ end }}
    </div>

    {{ if .Disks.IOStats }}
    <h3 style="margin-top:16px;">Disk I/O</h3>
    <table class="tbl">
      <thead>
        <tr><th>Device</th><th>Read Cnt</th><th>Write Cnt</th><th>Read Bytes</th><th>Write Bytes</th><th>Read Time (ms)</th><th>Write Time (ms)</th></tr>
      </thead>
      <tbody>
      {{ range .Disks.IOStats }}
        <tr>
          <td>{{ .Name }}</td>
          <td>{{ .ReadCount }}</td>
          <td>{{ .WriteCount }}</td>
          <td>{{ .ReadBytes }}</td>
          <td>{{ .WriteBytes }}</td>
          <td>{{ .ReadTimeMS }}</td>
          <td>{{ .WriteTimeMS }}</td>
        </tr>
      {{ end }}
      </tbody>
    </table>
    {{ end }}
  </section>

  <section class="card" style="margin-top:16px;">
    <h2>Recent OS Errors (AIX errpt)</h2>
    {{ if .Errors.Note }}<div class="small" style="margin-bottom:8px;">{{ .Errors.Note }}</div>{{ end }}
    <div class="grid">
      <div class="card" style="padding:12px;">
        <h2>Summary (All Time)</h2>
        <div class="kv">
          <div>Total</div><div>{{ .Errors.AllTimeSummary.Total }}</div>
          <div>Permanent</div><div>{{ .Errors.AllTimeSummary.Permanent }}</div>
          <div>Temporary</div><div>{{ .Errors.AllTimeSummary.Temporary }}</div>
          <div>Informational</div><div>{{ .Errors.AllTimeSummary.Informational }}</div>
          <div>Unknown</div><div>{{ .Errors.AllTimeSummary.Unknown }}</div>
        </div>
      </div>
      <div class="card" style="padding:12px;">
        <h2>Summary (Last 24h)</h2>
        <div class="kv">
          <div>Total</div><div>{{ .Errors.Last24hSummary.Total }}</div>
          <div>Permanent</div><div>{{ .Errors.Last24hSummary.Permanent }}</div>
          <div>Temporary</div><div>{{ .Errors.Last24hSummary.Temporary }}</div>
          <div>Informational</div><div>{{ .Errors.Last24hSummary.Informational }}</div>
          <div>Unknown</div><div>{{ .Errors.Last24hSummary.Unknown }}</div>
        </div>
      </div>
    </div>

    {{ if .Errors.Last24hFullBlock }}
      <h3 style="margin-top:12px;">All Errors in the Last 24 Hours</h3>
      <pre>{{ .Errors.Last24hFullBlock }}</pre>
    {{ else }}
      <div class="small" style="margin-top:8px;">No errors in the last 24 hours.</div>
    {{ end }}
  </section>

  <div class="footer">Generated on {{ .GeneratedAt.Format "2006-01-02 15:04:05 -0700" }}</div>
</div>
</body>
</html>`
