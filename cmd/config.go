package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/lint/support"
	"sigs.k8s.io/kustomize/kyaml/yaml"

	"github.com/dollarshaveclub/acyl/pkg/persistence"

	"github.com/rivo/tview"

	"github.com/dollarshaveclub/acyl/pkg/eventlogger"

	"github.com/alecthomas/chroma/quick"
	"github.com/dollarshaveclub/acyl/pkg/ghclient"
	"github.com/dollarshaveclub/acyl/pkg/models"
	"github.com/dollarshaveclub/acyl/pkg/nitro/meta"
	"github.com/dollarshaveclub/acyl/pkg/nitro/metahelm"
	"github.com/dollarshaveclub/acyl/pkg/nitro/metrics"
	"github.com/dollarshaveclub/metahelm/pkg/dag"
	metahelmlib "github.com/dollarshaveclub/metahelm/pkg/metahelm"
	"github.com/gdamore/tcell"
	"github.com/spf13/afero"
	"gopkg.in/src-d/go-billy.v4"
	"gopkg.in/src-d/go-billy.v4/osfs"

	"github.com/spf13/cobra"
)

// configCmd represents the config command
var configCmd = &cobra.Command{
	Use:   "config",
	Short: "local testing and development tools for acyl.yml",
	Long: `config and subcommands are used to do local validation and testing
of acyl.yml configurations`,
}

var configInfoCmd = &cobra.Command{
	Use:   "info",
	Short: "get summary information from an acyl.yml",
	Long: `Parses, validates and displays summary information about the acyl.yml in the current directory
(which must be a valid git repo with GitHub remotes). Branch matching will use the currently checked-out branch and the value 
passed in for the base-branch flag.

Paths provided by --search-paths will be recursively searched for valid git repositories containing GitHub remotes,
and if found, they will be used as repo dependencies if those GitHub repository names are referenced by acyl.yml. Any branches present in the
local repositories will be used for branch-matching purposes (they do not need to exist in the remote GitHub repo).

Any repo or chart_repo_path dependencies that are referenced in acyl.yml (or transitively included acyl.yml files) but not found in the local filesystem will be accessed via the GitHub API.
Ensure that you have a valid GitHub token in the environment variable GITHUB_TOKEN and that it has at least read permissions
for the repositories referenced that are not present locally.`,
	Run: configInfo,
}

var configCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "quicly validate an acyl.yml",
	Long: `Parses and validates the acyl.yml in the current directory and exits with code 0 if successful, or 1 if an error was detected.
This is intended for use in scripts or CI as a check that acyl.yml is valid.

Branch matching will use the currently checked-out branch and the value passed in for the base-branch flag.

Paths provided by --search-paths will be recursively searched for valid git repositories containing GitHub remotes,
and if found, they will be used as repo dependencies if those GitHub repository names are referenced by acyl.yml. Any branches present in the
local repositories will be used for branch-matching purposes (they do not need to exist in the remote GitHub repo).

Any repo or chart_repo_path dependencies that are referenced in acyl.yml (or transitively included acyl.yml files) but not found in the local filesystem will be accessed via the GitHub API.
Ensure that you have a valid GitHub token in the environment variable GITHUB_TOKEN and that it has at least read permissions
for the repositories referenced that are not present locally.`,
	Run: configCheck,
}

var repoSearchPaths []string
var workingTreeRepos []string
var localRepos map[string]string
var githubHostname, baseBranch string
var shell, dotPath, openPath string
var verbose, triggeringRepoUsesWorkingTree bool

func init() {
	// info
	configInfoCmd.Flags().StringVar(&shell, "shell", "/bin/bash -c", "Path to command shell plus command prefix")
	configInfoCmd.Flags().StringVar(&dotPath, "dot-path", "dot", "Path to Graphviz dot")
	switch runtime.GOOS {
	case "darwin":
		openPath = "open"
	case "windows":
		openPath = "start"
	default:
		openPath = "xdg-open"
	}
	configInfoCmd.Flags().StringVar(&openPath, "open-path", openPath, "Path to OS-specific open command")
	// check

	// test (create/update/delete)
	// (see test.go)

	configCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Verbose output")
	configCmd.PersistentFlags().StringSliceVar(&repoSearchPaths, "search-paths", []string{"."}, "comma-separated list of paths to search for git repositories")
	configCmd.PersistentFlags().StringSliceVar(&workingTreeRepos, "working-tree-repos", []string{}, "comma-separated list of repo names to use the working tree instead of commits, if present locally")
	configCmd.PersistentFlags().BoolVar(&triggeringRepoUsesWorkingTree, "triggering-repo-working-tree", true, "Triggering repo always uses working tree instead of commits")
	configCmd.PersistentFlags().StringVar(&githubHostname, "github-hostname", "github.com", "GitHub hostname in git repo SSH remotes")
	configCmd.PersistentFlags().StringVar(&baseBranch, "base-branch", "master", "Base branch to use for branch-matching logic")
	configCmd.PersistentFlags().StringVar(&testEnvCfg.kubeCfgPath, "kubecfg", "", "Path to kubeconfig (overrides KUBECONFIG)")

	configCmd.AddCommand(configTestCmd)
	configCmd.AddCommand(configInfoCmd)
	configCmd.AddCommand(configCheckCmd)
	RootCmd.AddCommand(configCmd)
}

func generateLocalMetaGetter(dl persistence.DataLayer, scb ghclient.StatusCallback) (*meta.DataGetter, ghclient.LocalRepoInfo, string, context.Context) {
	var lf func(string, ...interface{})
	var elsink io.Writer
	logw := ioutil.Discard
	if verbose {
		lf = log.Printf
		elsink = os.Stdout
		logw = os.Stdout
	}
	log.SetOutput(os.Stderr)
	if os.Getenv("GITHUB_TOKEN") == "" {
		log.Fatalf("GITHUB_TOKEN is empty: make sure you have that environment variable set with a valid token")
	}
	logger = log.New(logw, "", log.LstdFlags)
	f := ghclient.RepoFinder{
		GitHubHostname: githubHostname,
		FSFunc:         func(path string) afero.Fs { return afero.NewOsFs() },
		LF:             lf,
	}
	logger.Printf("scanning for GitHub repos in paths: %v", repoSearchPaths)
	repos, err := f.Find(repoSearchPaths)
	if err != nil {
		log.Fatalf("error searching for repos: %v", err)
	}
	localRepos = repos
	wd, err := os.Getwd()
	if err != nil {
		log.Fatalf("error getting working directory: %v", err)
	}
	logger.Printf("processing current working directory (must be a git repo w/ GitHub remotes): %v", wd)
	ri, err := ghclient.RepoInfo(afero.NewOsFs(), wd, githubHostname)
	if err != nil {
		log.Fatalf("error getting repo info for current directory: %v", err)
	}
	el := &eventlogger.Logger{
		ExcludeID: true,
		ID:        uuid.Must(uuid.NewRandom()),
		Sink:      elsink,
		DL:        dl,
	}
	if err := el.Init([]byte{}, ri.GitHubRepoName, testEnvCfg.pullRequest); err != nil {
		log.Fatalf("error initializing event: %v", err)
	}
	ctx := eventlogger.NewEventLoggerContext(context.Background(), el)
	if triggeringRepoUsesWorkingTree {
		workingTreeRepos = append(workingTreeRepos, ri.GitHubRepoName)
	}
	lw := &ghclient.LocalWrapper{
		WorkingTreeRepos:  workingTreeRepos,
		Backend:           ghclient.NewGitHubClient(os.Getenv("GITHUB_TOKEN")),
		FSFunc:            func(path string) billy.Filesystem { return osfs.New(path) },
		RepoPathMap:       repos,
		SetStatusCallback: scb,
	}
	// override refs (and therefore docker image tags) for local repos so we don't use the HEAD commit SHA
	// for working tree changes
	// https://stackoverflow.com/questions/22892120/how-to-generate-a-random-string-of-a-fixed-length-in-go
	randString := func(n int) string {
		rand.Seed(time.Now().UTC().UnixNano())
		chars := []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890")
		out := make([]rune, n)
		for i := range out {
			out[i] = chars[rand.Intn(len(chars))]
		}
		return string(out)
	}
	repoRefOverrides := make(map[string]string, len(workingTreeRepos))
	for _, wtr := range workingTreeRepos {
		// we don't know the image repo name yet, so we can't reliably make sure that the image tag
		// stays <= 128 characters. Image build/push will fail if the tag is too long.
		repoRefOverrides[wtr] = "local-" + randString(12)
	}
	if triggeringRepoUsesWorkingTree {
		ri.HeadSHA = repoRefOverrides[ri.GitHubRepoName]
	}
	return &meta.DataGetter{RepoRefOverrides: repoRefOverrides, RC: lw, FS: osfs.New("")}, ri, wd, ctx
}

func configCheck(cmd *cobra.Command, args []string) {
	perr := func(msg string, args ...interface{}) {
		fmt.Fprintf(os.Stderr, msg+"\n", args...)
	}
	var err error
	defer func() {
		if err != nil {
			os.Exit(1)
		}
	}()
	mg, ri, wd, ctx := generateLocalMetaGetter(persistence.NewFakeDataLayer(), nil)
	rrd := models.RepoRevisionData{
		PullRequest:  999,
		Repo:         ri.GitHubRepoName,
		BaseBranch:   baseBranch,
		BaseSHA:      "",
		SourceBranch: ri.HeadBranch,
		SourceSHA:    ri.HeadSHA,
		User:         "john.doe",
	}
	logger.Printf("processing %v", filepath.Join(wd, "acyl.yml"))
	rc, err := mg.Get(ctx, rrd)
	if err != nil {
		perr("error processing config: %v", err)
		return
	}
	tempd, err := ioutil.TempDir("", "acyl-config-check")
	if err != nil {
		perr("error creating temp file: %v", err)
		return
	}
	cl, err := mg.FetchCharts(ctx, rc, tempd)
	if err != nil {
		perr("error fetching charts: %v", err)
		return
	}
	ci, err := metahelm.NewChartInstallerWithClientsetFromContext(nil, persistence.NewFakeDataLayer(), osfs.New(""), &metrics.FakeCollector{}, k8sConfig.GroupBindings, k8sConfig.PrivilegedRepoWhitelist, k8sConfig.SecretInjections, testEnvCfg.kubeCfgPath, helmClientConfig)
	if err != nil {
		perr("error creating chart installer: %v", err)
		return
	}
	mcloc := metahelm.ChartLocations{}
	for k, v := range cl {
		mcloc[k] = metahelm.ChartLocation{
			ChartPath:   v.ChartPath,
			VarFilePath: v.VarFilePath,
		}
	}
	_, err = ci.GenerateCharts(ctx, "nitro-12345-some-name", &metahelm.EnvInfo{RC: rc, Env: &models.QAEnvironment{Name: "some-name", Repo: rc.Application.Repo}}, mcloc)
	if err != nil {
		perr("error generating charts: %v", err)
		return
	}
}

func configInfo(cmd *cobra.Command, args []string) {
	mg, ri, wd, ctx := generateLocalMetaGetter(persistence.NewFakeDataLayer(), nil)
	rrd := models.RepoRevisionData{
		PullRequest:  999,
		Repo:         ri.GitHubRepoName,
		BaseBranch:   baseBranch,
		BaseSHA:      "",
		SourceBranch: ri.HeadBranch,
		SourceSHA:    ri.HeadSHA,
		User:         "john.doe",
	}
	logger.Printf("processing %v", filepath.Join(wd, "acyl.yml"))
	rc, err := mg.Get(ctx, rrd)
	os.Exit(displayInfoTerminal(rc, err, mg))
}

// detailsType enumerates the content type currently occupying the details grid pane
type detailsType int

const (
	emptyDetailsType detailsType = iota
	triggeringRepoDetailsType
	dependencyDetailsType
	dagDetailsType
)

func displayInfoTerminal(rc *models.RepoConfig, err error, mg meta.Getter) int {
	ctx := eventlogger.NewEventLoggerContext(context.Background(), &eventlogger.Logger{Sink: ioutil.Discard})
	app := tview.NewApplication()
	errorModalText := func(msg, help string, err error) string {
		return "[red::b]" + msg + "\n\n[white::-]" + tview.Escape(err.Error()) + "\n\n[yellow::b]" + help
	}
	if err != nil {
		modal := tview.NewModal().
			SetText(errorModalText("Error Processing Config", "Fix your acyl.yml (or dependencies) and retry.", err)).
			AddButtons([]string{"Quit"}).
			SetDoneFunc(func(buttonIndex int, buttonLabel string) { app.Stop() })
		app.SetRoot(modal, false).SetFocus(modal).Run()
		log.Printf("error: %v", err) // always print the error even if not in verbose mode
		return 1
	}

	mcharts := map[string]metahelmlib.Chart{}
	var currentDetailsType detailsType
	var currentTreeNode *tview.TreeNode

	details := tview.NewTable()
	tree := tview.NewTreeView()
	grid := tview.NewGrid()
	pages := tview.NewPages()

	chartmodal := tview.NewModal().SetText("Loading charts...")
	chartmodalgrid := tview.NewGrid()
	chartmodalgrid.SetColumns(0, 20, 0).
		SetRows(0, 5, 0)
	addToChartModalGrid := func(m *tview.Modal) {
		chartmodalgrid.RemoveItem(chartmodal)
		chartmodalgrid.AddItem(m, 1, 1, 1, 1, 0, 0, true)
	}
	addToChartModalGrid(chartmodal)

	chartvaltxt := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignLeft)
	chartvaltxt.SetBorder(true).SetTitle("Rendered Chart Values (esc to go back)")
	pages.AddPage("chart_vals", chartvaltxt, true, false)

	chartlinttxt := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignLeft)
	chartlinttxt.SetBorder(true).SetTitle("Chart Linter (esc to go back)")
	pages.AddPage("chart_lint", chartlinttxt, true, false)

	errorModal := func(msg, help string, err error) {
		errmodal := tview.NewModal().SetText(errorModalText(msg, help, err)).
			AddButtons([]string{"OK"}).SetDoneFunc(func(int, string) {
			pages.SwitchToPage("main")
		})
		addToChartModalGrid(errmodal)
		pages.ShowPage("modal")
		app.SetFocus(errmodal)
		app.Draw()
	}

	infoModal := func(msg string, fn func()) {
		infomodal := tview.NewModal().SetText(msg).
			AddButtons([]string{"OK"}).SetDoneFunc(func(int, string) {
			pages.SwitchToPage("main")
			if fn != nil {
				fn()
			}
		})
		addToChartModalGrid(infomodal)
		pages.ShowPage("modal")
		app.SetFocus(infomodal)
		app.Draw()
	}

	getChartVals := func() (metahelmlib.Chart, error) {
		ref := currentTreeNode.GetReference()
		if ref == nil {
			return metahelmlib.Chart{}, errors.New("ref is nil!")
		}

		var mc metahelmlib.Chart
		var ok bool

		switch v := ref.(type) {
		case *models.RepoConfig:
			mc, ok = mcharts[models.GetName(v.Application.Repo)]
			if !ok {
				return metahelmlib.Chart{}, errors.New("chart location not found")
			}
		case models.RepoConfigDependency:
			mc, ok = mcharts[v.Name]
			if !ok {
				return metahelmlib.Chart{}, errors.New("chart location not found")
			}
		default:
			return metahelmlib.Chart{}, errors.New("bad type for ref: " + fmt.Sprintf("%T", ref))
		}

		return mc, nil
	}

	valBtn := tview.NewButton("View Rendered Chart Values").SetSelectedFunc(func() {
		chartvaltxt.Clear()

		defer func() {
			pages.SwitchToPage("chart_vals")
			app.SetFocus(chartvaltxt)
			app.Draw()
		}()

		mc, err := getChartVals()
		if err != nil {
			chartvaltxt.SetText("[red::b]" + err.Error() + "[-::-]")
			return
		}
		err = quick.Highlight(tview.ANSIWriter(chartvaltxt), string(mc.ValueOverrides), "YAML", "terminal256", "monokai")
		if err != nil {
			chartvaltxt.SetText("[red::b]Error syntax highlighting: [white:-:-]" + err.Error())
		}
	})
	lintBtn := tview.NewButton("Chart Linter").SetSelectedFunc(func() {
		chartlinttxt.Clear()

		defer func() {
			pages.SwitchToPage("chart_lint")
			app.SetFocus(chartlinttxt)
			app.Draw()
		}()

		mc, err := getChartVals()
		if err != nil {
			chartlinttxt.SetText("[red::b]" + err.Error() + "[-::-]")
			return
		}

		severityString := func(severity int) (string, string) {
			switch severity {
			case support.ErrorSev:
				return "ERROR", "red"
			case support.InfoSev:
				return "INFO", "green"
			case support.WarningSev:
				return "WARNING", "yellow"
			case support.UnknownSev:
				return "UNKNOWN", "cyan"
			}
			return "UNKNOWN", "cyan"
		}

		vos := make(map[string]interface{})
		yaml.Unmarshal(mc.ValueOverrides, &vos) // if error, empty value overrides

		log.SetOutput(ioutil.Discard)
		l := action.NewLint()
		l.Strict = true
		l.Namespace = "nitro-12345-some-name"
		lr := l.Run([]string{mc.Location}, vos)
		if verbose {
			log.SetOutput(os.Stderr)
		}

		fmt.Fprintf(chartlinttxt, "[::b]Lint Messages:[::-] %v\n", len(lr.Messages))
		fmt.Fprintf(chartlinttxt, "[::u]Messages:[::-]\n\n")
		for _, m := range lr.Messages {
			s, c := severityString(m.Severity)
			fmt.Fprintf(chartlinttxt, "Severity: [%v::b]%v[-::-]\n", c, s)
			fmt.Fprintf(chartlinttxt, "Path: %v\n", m.Path)
			fmt.Fprintf(chartlinttxt, "Message: [::b]%v[::-]\n\n", m.Err)
		}
	})

	getDAGOG := func() (dag.ObjectGraph, error) {
		objs := []dag.GraphObject{}
		for k := range mcharts {
			v := mcharts[k]
			objs = append(objs, &v)
		}
		og := dag.ObjectGraph{}
		return og, og.Build(objs)
	}

	dagBtn := tview.NewButton("Display Graph").SetSelectedFunc(func() {
		og, err := getDAGOG()
		if err != nil {
			errorModal("Object Graph Error", "Check dependency configuration.", err)
			return
		}
		b, err := og.Dot(`"` + rc.Application.Repo + `"`)
		if err != nil {
			errorModal("Error Generating Graph", "Check dependency configuration.", err)
			return
		}
		f, err := ioutil.TempFile("", "acyl-metahelm-dag")
		if err != nil {
			errorModal("Error Creating Temp File", "Check your disk.", err)
			return
		}
		f.Write(b)
		f.Close()
		fn := f.Name()
		opencmd := fmt.Sprintf("%v %v -Tpng -o %v.png && %v %v.png", dotPath, fn, fn, openPath, fn)
		shellsl := strings.Split(shell, " ")
		cmdsl := append(shellsl, opencmd)
		c := exec.Command(cmdsl[0], cmdsl[1:]...)
		if out, err := c.CombinedOutput(); err != nil {
			err = fmt.Errorf("%v: %v", err, string(out))
			errorModal("Error Running Command", strings.Join(cmdsl, " "), err)
			return
		}
		infoModal("Displayed Graph: "+fn+".png", func() { os.Remove(fn); os.Remove(fn + ".png") })
	})

	renderTriggeringRepo := func() {
		var row int
		addRow := func(name, value string) {
			details.SetCellSimple(row, 0, "[white::b]"+name+"[white::-]")
			details.SetCellSimple(row, 1, "[white::-]"+value)
			row++
		}
		details.Clear()
		details.SetTitle("[white::b]Triggering Repo[white::-]")

		addRow("Repo:", rc.Application.Repo)
		var sfx string
		if triggeringRepoUsesWorkingTree {
			sfx = " [green::b](WORKING TREE)[white::-]"
		}
		wd, _ := os.Getwd()
		addRow("Path:", wd+sfx)
		addRow("Branch:", rc.Application.Branch)
		addRow("Commit:", rc.Application.Ref)
		addRow("Image Repo:", rc.Application.Image)
		addRow("Chart Image Tag Value:", rc.Application.ChartTagValue)
		addRow("Chart Namespace Value:", rc.Application.NamespaceValue)
		addRow("Chart Environment Name Value:", rc.Application.EnvNameValue)

		var cp string
		if rc.Application.ChartPath != "" {
			cp = rc.Application.ChartPath
		} else {
			cp = rc.Application.ChartRepoPath
		}
		addRow("Chart:", cp)

		var vp string
		if rc.Application.ChartVarsPath != "" {
			vp = rc.Application.ChartVarsPath
		} else {
			vp = rc.Application.ChartVarsRepoPath
		}
		addRow("Chart Vars:", vp)

		if len(rc.Application.ValueOverrides) > 0 {
			details.SetCellSimple(row, 0, "[white::b]Chart Value Overrides:[white::-]")
			for i, vor := range rc.Application.ValueOverrides {
				details.SetCellSimple(row+i, 1, "[white::-]"+tview.Escape(vor))
			}
		}
		grid.RemoveItem(dagBtn)
		grid.AddItem(valBtn, 2, 1, 1, 1, 0, 0, true)
		grid.AddItem(lintBtn, 3, 1, 1, 1, 0, 0, true)
	}

	renderDependency := func(d models.RepoConfigDependency) {
		var row int
		addRow := func(name, value string) {
			details.SetCellSimple(row, 0, "[white::b]"+name+"[white::-]")
			details.SetCellSimple(row, 1, "[white::-]"+value)
			row++
		}
		details.Clear()
		details.SetTitle("[white::b]Dependency[white::-]")
		addRow("Name:", d.Name)
		switch {
		case d.Repo != "":
			if p, ok := localRepos[d.Repo]; ok {
				var sfx string
				for _, wtr := range workingTreeRepos {
					if d.Repo == wtr {
						sfx = " [green::b](WORKING TREE)[white::-]"
					}
				}
				addRow("Path:", p+sfx)
			} else {
				addRow("URL:", "https://github.com/"+d.Repo)
			}
			addRow("Repo:", d.Repo)
			addRow("Type:", "repo")
		case d.AppMetadata.ChartPath != "":
			addRow("Type:", "chart_path")
		case d.AppMetadata.ChartRepoPath != "":
			addRow("Type:", "chart_repo_path")
		default:
			addRow("Type:", "[red::]unknown[-::]")
		}
		if d.Parent != "" {
			addRow("Parent:", d.Parent)
		}
		if d.BranchMatchable() {
			addRow("Branch Matching:", fmt.Sprintf("%v", !d.DisableBranchMatch))
			if d.DefaultBranch != "" {
				addRow("Default Branch:", d.DefaultBranch)
			}
		}
		if d.AppMetadata.Branch != "" {
			addRow("Branch:", d.AppMetadata.Branch)
		}
		addRow("Commit:", d.AppMetadata.Ref)
		if d.AppMetadata.Image != "" {
			addRow("Image Repo:", d.AppMetadata.Image)
			addRow("Chart Image Tag Value:", d.AppMetadata.ChartTagValue)
		}
		addRow("Chart Namespace Value:", d.AppMetadata.NamespaceValue)
		addRow("Chart Environment Name Value:", d.AppMetadata.EnvNameValue)

		var cp string
		if d.AppMetadata.ChartPath != "" {
			cp = d.AppMetadata.ChartPath
		} else {
			cp = d.AppMetadata.ChartRepoPath
		}
		addRow("Chart:", cp)

		var vp string
		if d.AppMetadata.ChartVarsPath != "" {
			vp = d.AppMetadata.ChartVarsPath
		} else {
			vp = d.AppMetadata.ChartVarsRepoPath
		}
		addRow("Chart Vars:", vp)

		if len(d.Requires) > 0 {
			details.SetCellSimple(row, 0, "[white::b]Requires:[white::-]")
			for _, r := range d.Requires {
				details.SetCellSimple(row, 1, "[white::-]"+tview.Escape(r))
			}
			row++
		}

		if len(d.AppMetadata.ValueOverrides) > 0 {
			details.SetCellSimple(row, 0, "[white::b]Chart Value Overrides:[white::-]")
			for _, vor := range d.AppMetadata.ValueOverrides {
				details.SetCellSimple(row, 1, "[white::-]"+tview.Escape(vor))
			}
			row++
		}
		grid.RemoveItem(dagBtn)
		grid.AddItem(valBtn, 2, 1, 1, 1, 0, 0, true)
		grid.AddItem(lintBtn, 3, 1, 1, 1, 0, 0, true)
	}

	displayMetahelmDAG := func() {
		og, err := getDAGOG()
		if err != nil {
			errorModal("Object Graph Error", "Check dependency configuration.", err)
			return
		}
		r, lvls, err := og.Info()
		if err != nil {
			errorModal("Object Graph Info Error", "Check your dependency requirement configuration.", err)
			return
		}
		var row int
		addRow := func(name, value string) {
			details.SetCellSimple(row, 0, "[white::b]"+name+"[white::-]")
			details.SetCellSimple(row, 1, "[white::-]"+value)
			row++
		}
		details.Clear()
		addRow("Graph Root:", r.Name())
		j := 1
		for i := len(lvls) - 1; i >= 0; i-- {
			details.SetCellSimple(row, 0, "[white::b]Install/Upgrade Phase "+fmt.Sprintf("%v", j)+":[white::-]")
			for k, obj := range lvls[i] {
				details.SetCellSimple(row+k, 1, "[white::-]"+obj.Name())
			}
			j++
			row += len(lvls[i])
		}
		grid.RemoveItem(valBtn)
		grid.RemoveItem(lintBtn)
		grid.AddItem(dagBtn, 2, 1, 2, 1, 0, 0, true)
	}

	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		// Switch focus on tab
		if event.Key() == tcell.KeyTAB {
			focus := app.GetFocus()
			if focus == nil {
				return event
			}
			switch v := focus.(type) {
			case *tview.Button:
				switch v.GetLabel() {
				case valBtn.GetLabel():
					app.SetFocus(lintBtn)
				case lintBtn.GetLabel():
					app.SetFocus(tree)
				case dagBtn.GetLabel():
					app.SetFocus(tree)
				}
			case *tview.Table:
				switch currentDetailsType {
				case dagDetailsType:
					app.SetFocus(dagBtn)
				case triggeringRepoDetailsType:
					app.SetFocus(valBtn)
				case dependencyDetailsType:
					app.SetFocus(valBtn)
				case emptyDetailsType:
					return event
				default:
					return event
				}
			case *tview.TreeView:
				if currentDetailsType == emptyDetailsType {
					return nil
				}
				app.SetFocus(details)
			default:
				break
			}
			return nil
		}
		if event.Key() == tcell.KeyESC {
			pages.SwitchToPage("main")
			app.SetFocus(tree)
			app.Draw()
		}
		return event
	})

	triggeringiswt := " [white::](LOCAL)"
	if triggeringRepoUsesWorkingTree {
		triggeringiswt = " [green::b](WORKING TREE)"
	}

	root := tview.NewTreeNode(rc.Application.Repo + " [yellow](triggering repo)" + triggeringiswt + "[white::-]").
		SetSelectable(true).
		SetReference(rc)
	tree.SetRoot(root).SetSelectedFunc(func(node *tview.TreeNode) {
		ref := node.GetReference()
		if ref == nil {
			return
		}
		currentTreeNode = node
		switch v := ref.(type) {
		case models.RepoConfigDependency:
			currentDetailsType = dependencyDetailsType
			renderDependency(v)
		case *models.RepoConfig:
			currentDetailsType = triggeringRepoDetailsType
			renderTriggeringRepo()
		case string:
			currentDetailsType = dagDetailsType
			displayMetahelmDAG()
		default:
			errorModal("Tree Node Reference Error", "Bug!", fmt.Errorf("ref is unexpected type: %T", ref))
		}
	}).SetCurrentNode(root)

	renderTree(root, rc)

	details.SetCellSimple(0, 1, "[::d](select tree item)[::-]")

	grid.SetRows(1, 0, 1, 1, 1).SetColumns(0, 0).SetBorders(true).
		AddItem(tview.NewTextView().
			SetTextAlign(tview.AlignCenter).
			SetDynamicColors(true).
			SetText("[yellow::b]Acyl Environment Config: [white::-]"+rc.Application.Repo), 0, 0, 1, 2, 0, 0, false).
		AddItem(tree, 1, 0, 3, 1, 0, 0, true).
		AddItem(details, 1, 1, 1, 1, 0, 0, false).
		AddItem(tview.NewTextView().
			SetTextAlign(tview.AlignCenter).
			SetText("enter to select item - tab to switch focus - ctrl+c to exit"), 4, 0, 1, 2, 0, 0, false)

	pages.AddPage("main", grid, true, true)
	pages.AddPage("modal", chartmodalgrid, false, true) // must add after main so main is visible behind it

	tempd, err := ioutil.TempDir("", "acyl-config")
	if err != nil {
		log.Printf("error creating temp dir: %v", err)
		return 1
	}
	defer os.RemoveAll(tempd)

	go func() {
		pages.ShowPage("modal")
		cl, err := mg.FetchCharts(ctx, rc, tempd)
		if err != nil {
			errorModal("Error Processing Charts", "Check your chart configuration.", err)
			return
		}
		ci, err := metahelm.NewChartInstallerWithClientsetFromContext(nil, persistence.NewFakeDataLayer(), osfs.New(""), &metrics.FakeCollector{}, k8sConfig.GroupBindings, k8sConfig.PrivilegedRepoWhitelist, k8sConfig.SecretInjections, testEnvCfg.kubeCfgPath, helmClientConfig)
		if err != nil {
			errorModal("Error Instantiating Chart Installer", "Bug!", err)
			return
		}
		mcloc := metahelm.ChartLocations{}
		for k, v := range cl {
			mcloc[k] = metahelm.ChartLocation{
				ChartPath:   v.ChartPath,
				VarFilePath: v.VarFilePath,
			}
		}
		charts, err := ci.GenerateCharts(ctx, "nitro-12345-some-name", &metahelm.EnvInfo{RC: rc, Env: &models.QAEnvironment{Name: "some-name", Repo: rc.Application.Repo}}, mcloc)
		if err != nil {
			errorModal("Error Generating Metahelm Charts", "Check your chart configuration.", err)
			return
		}
		for _, c := range charts {
			mcharts[c.Title] = c
		}
		pages.HidePage("modal")
		app.SetFocus(tree)
		app.Draw()
	}()
	if err := app.SetRoot(pages, true).SetFocus(pages).Run(); err != nil {
		log.Printf("error starting terminal UI: %v", err)
		return 1
	}
	return 0
}

func renderTree(root *tview.TreeNode, rc *models.RepoConfig) {
	root.AddChild(tview.NewTreeNode("[white::b]Metahelm DAG").SetSelectable(true).SetReference("DAG"))

	depmap := make(map[string]*tview.TreeNode, rc.Dependencies.Count())

	wtrm := make(map[string]struct{}, len(workingTreeRepos))
	for _, r := range workingTreeRepos {
		wtrm[r] = struct{}{}
	}

	nodename := func(d models.RepoConfigDependency) string {
		if d.Repo != "" {
			if _, ok := wtrm[d.Repo]; ok {
				return d.Name + " [green::b](WORKING TREE)[white::-]"
			}
			if _, ok := localRepos[d.Repo]; ok {
				return d.Name + " [white::b](LOCAL)[white::-]"
			}
			return d.Name + " [white::b](REMOTE)[white::-]"
		}
		return d.Name
	}

	reqtargetmap := map[string]string{}
	for _, d := range rc.Dependencies.All() {
		for _, r := range d.Requires {
			reqtargetmap[r] = d.Name
		}
	}

	if len(rc.Dependencies.Direct) > 0 {
		ddeps := tview.NewTreeNode("[white::b]Direct Dependencies").SetSelectable(false)
		for _, d := range rc.Dependencies.Direct {
			parent := d.Parent
			if r, ok := reqtargetmap[d.Name]; ok {
				parent = r
			}
			if parent == "" {
				dn := tview.NewTreeNode(nodename(d)).SetSelectable(true).SetReference(d)
				ddeps.AddChild(dn)
				depmap[d.Name] = dn
			}
		}
		root.AddChild(ddeps)
	}
	if len(rc.Dependencies.Environment) > 0 {
		edeps := tview.NewTreeNode("[white::b]Environment Dependencies").SetSelectable(false)
		for _, d := range rc.Dependencies.Environment {
			parent := d.Parent
			if r, ok := reqtargetmap[d.Name]; ok {
				parent = r
			}
			if parent == "" {
				dn := tview.NewTreeNode(nodename(d)).SetSelectable(true).SetReference(d)
				edeps.AddChild(dn)
				depmap[d.Name] = dn
			}
		}
		root.AddChild(edeps)
	}
	for {
		if len(depmap) == rc.Dependencies.Count() {
			return
		}
		for _, d := range rc.Dependencies.All() {
			parent := d.Parent
			if r, ok := reqtargetmap[d.Name]; ok {
				parent = r
			}
			if parent != "" {
				if n, ok := depmap[parent]; ok {
					dn := tview.NewTreeNode(nodename(d)).SetSelectable(true).SetReference(d)
					n.AddChild(dn)
					depmap[d.Name] = dn
				}
			}
		}
	}
}
