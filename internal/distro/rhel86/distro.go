package rhel86

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"path"
	"sort"
	"strings"

	"github.com/osbuild/osbuild-composer/internal/blueprint"
	"github.com/osbuild/osbuild-composer/internal/common"
	"github.com/osbuild/osbuild-composer/internal/disk"
	"github.com/osbuild/osbuild-composer/internal/distro"
	osbuild "github.com/osbuild/osbuild-composer/internal/osbuild2"
	"github.com/osbuild/osbuild-composer/internal/rpmmd"
)

const (
	// package set names

	// build package set name
	buildPkgsKey = "build"

	// main/common os image package set name
	osPkgsKey = "packages"

	// container package set name
	containerPkgsKey = "container"

	// installer package set name
	installerPkgsKey = "installer"

	// blueprint package set name
	blueprintPkgsKey = "blueprint"
)

var mountpointAllowList = []string{
	"/", "/var", "/opt", "/srv", "/usr", "/app", "/data", "/home", "/tmp",
}

type distribution struct {
	name               string
	product            string
	osVersion          string
	releaseVersion     string
	modulePlatformID   string
	vendor             string
	ostreeRefTmpl      string
	isolabelTmpl       string
	runner             string
	arches             map[string]distro.Arch
	defaultImageConfig *distro.ImageConfig
}

// RHEL-based OS image configuration defaults
var defaultDistroImageConfig = &distro.ImageConfig{
	Timezone: "America/New_York",
	Locale:   "en_US.UTF-8",
	Sysconfig: []*osbuild.SysconfigStageOptions{
		{
			Kernel: &osbuild.SysconfigKernelOptions{
				UpdateDefault: true,
				DefaultKernel: "kernel",
			},
			Network: &osbuild.SysconfigNetworkOptions{
				Networking: true,
				NoZeroConf: true,
			},
		},
	},
}

// distribution objects without the arches > image types
var distroMap = map[string]distribution{
	"rhel-86": {
		name:               "rhel-86",
		product:            "Red Hat Enterprise Linux",
		osVersion:          "8.6",
		releaseVersion:     "8",
		modulePlatformID:   "platform:el8",
		vendor:             "redhat",
		ostreeRefTmpl:      "rhel/8/%s/edge",
		isolabelTmpl:       "RHEL-8-6-0-BaseOS-%s",
		runner:             "org.osbuild.rhel86",
		defaultImageConfig: defaultDistroImageConfig,
	},
	"rhel-87": {
		name:               "rhel-87",
		product:            "Red Hat Enterprise Linux",
		osVersion:          "8.7",
		releaseVersion:     "8",
		modulePlatformID:   "platform:el8",
		vendor:             "redhat",
		ostreeRefTmpl:      "rhel/8/%s/edge",
		isolabelTmpl:       "RHEL-8-7-0-BaseOS-%s",
		runner:             "org.osbuild.rhel87",
		defaultImageConfig: defaultDistroImageConfig,
	},
	"centos-8": {
		name:               "centos-8",
		product:            "CentOS Stream",
		osVersion:          "8-stream",
		releaseVersion:     "8",
		modulePlatformID:   "platform:el8",
		vendor:             "centos",
		ostreeRefTmpl:      "centos/8/%s/edge",
		isolabelTmpl:       "CentOS-Stream-8-%s-dvd",
		runner:             "org.osbuild.centos8",
		defaultImageConfig: defaultDistroImageConfig,
	},
}

func (d *distribution) Name() string {
	return d.name
}

func (d *distribution) Releasever() string {
	return d.releaseVersion
}

func (d *distribution) ModulePlatformID() string {
	return d.modulePlatformID
}

func (d *distribution) OSTreeRef() string {
	return d.ostreeRefTmpl
}

func (d *distribution) ListArches() []string {
	archNames := make([]string, 0, len(d.arches))
	for name := range d.arches {
		archNames = append(archNames, name)
	}
	sort.Strings(archNames)
	return archNames
}

func (d *distribution) GetArch(name string) (distro.Arch, error) {
	arch, exists := d.arches[name]
	if !exists {
		return nil, errors.New("invalid architecture: " + name)
	}
	return arch, nil
}

func (d *distribution) addArches(arches ...architecture) {
	if d.arches == nil {
		d.arches = map[string]distro.Arch{}
	}

	// Do not make copies of architectures, as opposed to image types,
	// because architecture definitions are not used by more than a single
	// distro definition.
	for idx := range arches {
		d.arches[arches[idx].name] = &arches[idx]
	}
}

func (d *distribution) isRHEL() bool {
	return strings.HasPrefix(d.name, "rhel")
}

func (d *distribution) getDefaultImageConfig() *distro.ImageConfig {
	return d.defaultImageConfig
}

type architecture struct {
	distro           *distribution
	name             string
	imageTypes       map[string]distro.ImageType
	imageTypeAliases map[string]string
	legacy           string
	bootType         distro.BootType
}

func (a *architecture) Name() string {
	return a.name
}

func (a *architecture) ListImageTypes() []string {
	itNames := make([]string, 0, len(a.imageTypes))
	for name := range a.imageTypes {
		itNames = append(itNames, name)
	}
	sort.Strings(itNames)
	return itNames
}

func (a *architecture) GetImageType(name string) (distro.ImageType, error) {
	t, exists := a.imageTypes[name]
	if !exists {
		aliasForName, exists := a.imageTypeAliases[name]
		if !exists {
			return nil, errors.New("invalid image type: " + name)
		}
		t, exists = a.imageTypes[aliasForName]
		if !exists {
			panic(fmt.Sprintf("image type '%s' is an alias to a non-existing image type '%s'", name, aliasForName))
		}
	}
	return t, nil
}

func (a *architecture) addImageTypes(imageTypes ...imageType) {
	if a.imageTypes == nil {
		a.imageTypes = map[string]distro.ImageType{}
	}
	for idx := range imageTypes {
		it := imageTypes[idx]
		it.arch = a
		a.imageTypes[it.name] = &it
		for _, alias := range it.nameAliases {
			if a.imageTypeAliases == nil {
				a.imageTypeAliases = map[string]string{}
			}
			if existingAliasFor, exists := a.imageTypeAliases[alias]; exists {
				panic(fmt.Sprintf("image type alias '%s' for '%s' is already defined for another image type '%s'", alias, it.name, existingAliasFor))
			}
			a.imageTypeAliases[alias] = it.name
		}
	}
}

func (a *architecture) Distro() distro.Distro {
	return a.distro
}

type pipelinesFunc func(t *imageType, customizations *blueprint.Customizations, options distro.ImageOptions, repos []rpmmd.RepoConfig, packageSetSpecs map[string][]rpmmd.PackageSpec, rng *rand.Rand) ([]osbuild.Pipeline, error)

type packageSetFunc func(t *imageType) rpmmd.PackageSet

type imageType struct {
	arch               *architecture
	name               string
	nameAliases        []string
	filename           string
	mimeType           string
	packageSets        map[string]packageSetFunc
	defaultImageConfig *distro.ImageConfig
	kernelOptions      string
	defaultSize        uint64
	buildPipelines     []string
	payloadPipelines   []string
	exports            []string
	pipelines          pipelinesFunc

	// bootISO: installable ISO
	bootISO bool
	// rpmOstree: edge/ostree
	rpmOstree bool
	// bootable image
	bootable bool
	// If set to a value, it is preferred over the architecture value
	bootType distro.BootType
	// List of valid arches for the image type
	basePartitionTables distro.BasePartitionTableMap
}

func (t *imageType) Name() string {
	return t.name
}

func (t *imageType) Arch() distro.Arch {
	return t.arch
}

func (t *imageType) Filename() string {
	return t.filename
}

func (t *imageType) MIMEType() string {
	return t.mimeType
}

func (t *imageType) OSTreeRef() string {
	d := t.arch.distro
	if t.rpmOstree {
		return fmt.Sprintf(d.ostreeRefTmpl, t.arch.name)
	}
	return ""
}

func (t *imageType) Size(size uint64) uint64 {
	const MegaByte = 1024 * 1024
	// Microsoft Azure requires vhd images to be rounded up to the nearest MB
	if t.name == "vhd" && size%MegaByte != 0 {
		size = (size/MegaByte + 1) * MegaByte
	}
	if size == 0 {
		size = t.defaultSize
	}
	return size
}

func (t *imageType) getPackages(name string) rpmmd.PackageSet {
	getter := t.packageSets[name]
	if getter == nil {
		return rpmmd.PackageSet{}
	}

	return getter(t)
}

func (t *imageType) PackageSets(bp blueprint.Blueprint) map[string]rpmmd.PackageSet {
	// merge package sets that appear in the image type with the package sets
	// of the same name from the distro and arch
	mergedSets := make(map[string]rpmmd.PackageSet)

	imageSets := t.packageSets

	for name := range imageSets {
		mergedSets[name] = t.getPackages(name)
	}

	if _, hasPackages := imageSets[osPkgsKey]; !hasPackages {
		// should this be possible??
		mergedSets[osPkgsKey] = rpmmd.PackageSet{}
	}

	// every image type must define a 'build' package set
	if _, hasBuild := imageSets[buildPkgsKey]; !hasBuild {
		panic(fmt.Sprintf("'%s' image type has no '%s' package set defined", t.name, buildPkgsKey))
	}

	// blueprint packages
	bpPackages := bp.GetPackages()
	timezone, _ := bp.Customizations.GetTimezoneSettings()
	if timezone != nil {
		bpPackages = append(bpPackages, "chrony")
	}

	// if we have file system customization that will need to a new mount point
	// the layout is converted to LVM so we need to corresponding packages
	if !t.rpmOstree {
		archName := t.arch.Name()
		pt := t.basePartitionTables[archName]
		haveNewMountpoint := false

		if fs := bp.Customizations.GetFilesystems(); fs != nil {
			for i := 0; !haveNewMountpoint && i < len(fs); i++ {
				haveNewMountpoint = !pt.ContainsMountpoint(fs[i].Mountpoint)
			}
		}

		if haveNewMountpoint {
			bpPackages = append(bpPackages, "lvm2")
		}
	}

	// depsolve bp packages separately
	// bp packages aren't restricted by exclude lists
	mergedSets[blueprintPkgsKey] = rpmmd.PackageSet{Include: bpPackages}
	kernel := bp.Customizations.GetKernel().Name

	// add bp kernel to main OS package set to avoid duplicate kernels
	mergedSets[osPkgsKey] = mergedSets[osPkgsKey].Append(rpmmd.PackageSet{Include: []string{kernel}})
	return mergedSets

}

func (t *imageType) BuildPipelines() []string {
	return t.buildPipelines
}

func (t *imageType) PayloadPipelines() []string {
	return t.payloadPipelines
}

func (t *imageType) PayloadPackageSets() []string {
	return []string{blueprintPkgsKey}
}

func (t *imageType) Exports() []string {
	if len(t.exports) > 0 {
		return t.exports
	}
	return []string{"assembler"}
}

// getBootType returns the BootType which should be used for this particular
// combination of architecture and image type.
func (t *imageType) getBootType() distro.BootType {
	bootType := t.arch.bootType
	if t.bootType != distro.UnsetBootType {
		bootType = t.bootType
	}
	return bootType
}

func (t *imageType) supportsUEFI() bool {
	bootType := t.getBootType()
	if bootType == distro.HybridBootType || bootType == distro.UEFIBootType {
		return true
	}
	return false
}

func (t *imageType) getPartitionTable(
	mountpoints []blueprint.FilesystemCustomization,
	options distro.ImageOptions,
	rng *rand.Rand,
) (*disk.PartitionTable, error) {
	archName := t.arch.Name()

	basePartitionTable, exists := t.basePartitionTables[archName]

	if !exists {
		return nil, fmt.Errorf("unknown arch: " + archName)
	}

	imageSize := t.Size(options.Size)

	lvmify := !t.rpmOstree

	return disk.NewPartitionTable(&basePartitionTable, mountpoints, imageSize, lvmify, rng)
}

func (t *imageType) getDefaultImageConfig() *distro.ImageConfig {
	// ensure that image always returns non-nil default config
	imageConfig := t.defaultImageConfig
	if imageConfig == nil {
		imageConfig = &distro.ImageConfig{}
	}
	return imageConfig.InheritFrom(t.arch.distro.getDefaultImageConfig())

}

func (t *imageType) PartitionType() string {
	archName := t.arch.Name()
	basePartitionTable, exists := t.basePartitionTables[archName]
	if !exists {
		return ""
	}

	return basePartitionTable.Type
}

// local type for ostree commit metadata used to define commit sources
type ostreeCommit struct {
	Checksum string
	URL      string
}

func (t *imageType) Manifest(customizations *blueprint.Customizations,
	options distro.ImageOptions,
	repos []rpmmd.RepoConfig,
	packageSpecSets map[string][]rpmmd.PackageSpec,
	seed int64) (distro.Manifest, error) {

	if err := t.checkOptions(customizations, options); err != nil {
		return distro.Manifest{}, err
	}

	source := rand.NewSource(seed)
	// math/rand is good enough in this case
	/* #nosec G404 */
	rng := rand.New(source)

	pipelines, err := t.pipelines(t, customizations, options, repos, packageSpecSets, rng)
	if err != nil {
		return distro.Manifest{}, err
	}

	// flatten spec sets for sources
	allPackageSpecs := make([]rpmmd.PackageSpec, 0)
	for _, specs := range packageSpecSets {
		allPackageSpecs = append(allPackageSpecs, specs...)
	}

	// handle OSTree commit inputs
	var commits []ostreeCommit
	if options.OSTree.Parent != "" && options.OSTree.URL != "" {
		commits = []ostreeCommit{{Checksum: options.OSTree.Parent, URL: options.OSTree.URL}}
	}

	// handle inline sources
	inlineData := []string{}

	// FDO root certs, if any, are transmitted via an inline source
	if fdo := customizations.GetFDO(); fdo != nil && fdo.DiunPubKeyRootCerts != "" {
		inlineData = append(inlineData, fdo.DiunPubKeyRootCerts)
	}

	return json.Marshal(
		osbuild.Manifest{
			Version:   "2",
			Pipelines: pipelines,
			Sources:   t.sources(allPackageSpecs, commits, inlineData),
		},
	)
}

func (t *imageType) sources(packages []rpmmd.PackageSpec, ostreeCommits []ostreeCommit, inlineData []string) osbuild.Sources {
	sources := osbuild.Sources{}
	curl := &osbuild.CurlSource{
		Items: make(map[string]osbuild.CurlSourceItem),
	}
	for _, pkg := range packages {
		item := new(osbuild.URLWithSecrets)
		item.URL = pkg.RemoteLocation
		if pkg.Secrets == "org.osbuild.rhsm" {
			item.Secrets = &osbuild.URLSecrets{
				Name: "org.osbuild.rhsm",
			}
		}
		curl.Items[pkg.Checksum] = item
	}
	if len(curl.Items) > 0 {
		sources["org.osbuild.curl"] = curl
	}

	ostree := &osbuild.OSTreeSource{
		Items: make(map[string]osbuild.OSTreeSourceItem),
	}
	for _, commit := range ostreeCommits {
		item := new(osbuild.OSTreeSourceItem)
		item.Remote.URL = commit.URL
		ostree.Items[commit.Checksum] = *item
	}
	if len(ostree.Items) > 0 {
		sources["org.osbuild.ostree"] = ostree
	}

	if len(inlineData) > 0 {
		ils := osbuild.NewInlineSource()
		for _, data := range inlineData {
			ils.AddItem(data)
		}

		sources["org.osbuild.inline"] = ils
	}

	return sources
}

func isMountpointAllowed(mountpoint string) bool {
	for _, allowed := range mountpointAllowList {
		match, _ := path.Match(allowed, mountpoint)
		if match {
			return true
		}
		// ensure that only clean mountpoints
		// are valid
		if strings.Contains(mountpoint, "//") {
			return false
		}
		match = strings.HasPrefix(mountpoint, allowed+"/")
		if allowed != "/" && match {
			return true
		}
	}
	return false
}

// checkOptions checks the validity and compatibility of options and customizations for the image type.
func (t *imageType) checkOptions(customizations *blueprint.Customizations, options distro.ImageOptions) error {
	if t.bootISO && t.rpmOstree {
		if options.OSTree.Parent == "" {
			return fmt.Errorf("boot ISO image type %q requires specifying a URL from which to retrieve the OSTree commit", t.name)
		}

		if t.name == "edge-simplified-installer" {
			if err := customizations.CheckAllowed("InstallationDevice", "FDO"); err != nil {
				return fmt.Errorf("boot ISO image type %q contains unsupported blueprint customizations: %v", t.name, err)
			}
			if customizations.GetInstallationDevice() == "" {
				return fmt.Errorf("boot ISO image type %q requires specifying an installation device to install to", t.name)
			}
			if customizations.GetFDO() == nil {
				return fmt.Errorf("boot ISO image type %q requires specifying FDO configuration to install to", t.name)
			}
			if customizations.GetFDO().ManufacturingServerURL == "" {
				return fmt.Errorf("boot ISO image type %q requires specifying FDO.ManufacturingServerURL configuration to install to", t.name)
			}
			var diunSet int
			if customizations.GetFDO().DiunPubKeyHash != "" {
				diunSet++
			}
			if customizations.GetFDO().DiunPubKeyInsecure != "" {
				diunSet++
			}
			if customizations.GetFDO().DiunPubKeyRootCerts != "" {
				diunSet++
			}
			if diunSet != 1 {
				return fmt.Errorf("boot ISO image type %q requires specifying one of [FDO.DiunPubKeyHash,FDO.DiunPubKeyInsecure,FDO.DiunPubKeyRootCerts] configuration to install to", t.name)
			}
		} else if customizations != nil {
			return fmt.Errorf("boot ISO image type %q does not support blueprint customizations", t.name)
		}
	}

	if t.name == "edge-raw-image" && options.OSTree.Parent == "" {
		return fmt.Errorf("edge raw images require specifying a URL from which to retrieve the OSTree commit")
	}

	if kernelOpts := customizations.GetKernel(); kernelOpts.Append != "" && t.rpmOstree && (!t.bootable || t.bootISO) {
		return fmt.Errorf("kernel boot parameter customizations are not supported for ostree types")
	}

	mountpoints := customizations.GetFilesystems()

	if mountpoints != nil && t.rpmOstree {
		return fmt.Errorf("Custom mountpoints are not supported for ostree types")
	}

	invalidMountpoints := []string{}
	for _, m := range mountpoints {
		if !isMountpointAllowed(m.Mountpoint) {
			invalidMountpoints = append(invalidMountpoints, m.Mountpoint)
		}
	}

	if len(invalidMountpoints) > 0 {
		return fmt.Errorf("The following custom mountpoints are not supported %+q", invalidMountpoints)
	}

	return nil
}

// New creates a new distro object, defining the supported architectures and image types
func New() distro.Distro {
	return newDistro("rhel-86")
}

func NewHostDistro(name, modulePlatformID, ostreeRef string) distro.Distro {
	return newDistro("rhel-86")
}

// New creates a new distro object, defining the supported architectures and image types
func NewRHEL87() distro.Distro {
	return newDistro("rhel-87")
}

func NewRHEL87HostDistro(name, modulePlatformID, ostreeRef string) distro.Distro {
	return newDistro("rhel-87")
}

func NewCentos() distro.Distro {
	return newDistro("centos-8")
}

func NewCentosHostDistro(name, modulePlatformID, ostreeRef string) distro.Distro {
	return newDistro("centos-8")
}

func newDistro(distroName string) distro.Distro {
	const GigaByte = 1024 * 1024 * 1024

	rd := distroMap[distroName]

	// Architecture definitions
	x86_64 := architecture{
		name:     distro.X86_64ArchName,
		distro:   &rd,
		legacy:   "i386-pc",
		bootType: distro.HybridBootType,
	}

	aarch64 := architecture{
		name:     distro.Aarch64ArchName,
		distro:   &rd,
		bootType: distro.UEFIBootType,
	}

	ppc64le := architecture{
		distro:   &rd,
		name:     distro.Ppc64leArchName,
		legacy:   "powerpc-ieee1275",
		bootType: distro.LegacyBootType,
	}
	s390x := architecture{
		distro:   &rd,
		name:     distro.S390xArchName,
		bootType: distro.LegacyBootType,
	}

	// Shared Services
	edgeServices := []string{
		// TODO(runcom): move fdo-client-linuxapp.service to presets?
		"NetworkManager.service", "firewalld.service", "sshd.service", "fdo-client-linuxapp.service",
	}

	// Image Definitions
	edgeCommitImgType := imageType{
		name:        "edge-commit",
		nameAliases: []string{"rhel-edge-commit"},
		filename:    "commit.tar",
		mimeType:    "application/x-tar",
		packageSets: map[string]packageSetFunc{
			buildPkgsKey: edgeBuildPackageSet,
			osPkgsKey:    edgeCommitPackageSet,
		},
		defaultImageConfig: &distro.ImageConfig{
			EnabledServices: edgeServices,
		},
		rpmOstree:        true,
		pipelines:        edgeCommitPipelines,
		buildPipelines:   []string{"build"},
		payloadPipelines: []string{"ostree-tree", "ostree-commit", "commit-archive"},
		exports:          []string{"commit-archive"},
	}

	edgeOCIImgType := imageType{
		name:        "edge-container",
		nameAliases: []string{"rhel-edge-container"},
		filename:    "container.tar",
		mimeType:    "application/x-tar",
		packageSets: map[string]packageSetFunc{
			buildPkgsKey: edgeBuildPackageSet,
			osPkgsKey:    edgeCommitPackageSet,
			containerPkgsKey: func(t *imageType) rpmmd.PackageSet {
				return rpmmd.PackageSet{
					Include: []string{"nginx"},
				}
			},
		},
		defaultImageConfig: &distro.ImageConfig{
			EnabledServices: edgeServices,
		},
		rpmOstree:        true,
		bootISO:          false,
		pipelines:        edgeContainerPipelines,
		buildPipelines:   []string{"build"},
		payloadPipelines: []string{"ostree-tree", "ostree-commit", "container-tree", "container"},
		exports:          []string{"container"},
	}

	edgeRawImgType := imageType{
		name:        "edge-raw-image",
		nameAliases: []string{"rhel-edge-raw-image"},
		filename:    "image.raw.xz",
		mimeType:    "application/xz",
		packageSets: map[string]packageSetFunc{
			buildPkgsKey: edgeRawImageBuildPackageSet,
		},
		defaultSize:         10 * GigaByte,
		rpmOstree:           true,
		bootable:            true,
		bootISO:             false,
		pipelines:           edgeRawImagePipelines,
		buildPipelines:      []string{"build"},
		payloadPipelines:    []string{"image-tree", "image", "archive"},
		exports:             []string{"archive"},
		basePartitionTables: edgeBasePartitionTables,
	}

	edgeInstallerImgType := imageType{
		name:        "edge-installer",
		nameAliases: []string{"rhel-edge-installer"},
		filename:    "installer.iso",
		mimeType:    "application/x-iso9660-image",
		packageSets: map[string]packageSetFunc{
			// TODO: non-arch-specific package set handling for installers
			// This image type requires build packages for installers and
			// ostree/edge.  For now we only have x86-64 installer build
			// package sets defined.  When we add installer build package sets
			// for other architectures, this will need to be moved to the
			// architecture and the merging will happen in the PackageSets()
			// method like the other sets.
			buildPkgsKey:     edgeInstallerBuildPackageSet,
			osPkgsKey:        edgeCommitPackageSet,
			installerPkgsKey: edgeInstallerPackageSet,
		},
		defaultImageConfig: &distro.ImageConfig{
			EnabledServices: edgeServices,
		},
		rpmOstree:        true,
		bootISO:          true,
		pipelines:        edgeInstallerPipelines,
		buildPipelines:   []string{"build"},
		payloadPipelines: []string{"anaconda-tree", "bootiso-tree", "bootiso"},
		exports:          []string{"bootiso"},
	}

	edgeSimplifiedInstallerImgType := imageType{
		name:        "edge-simplified-installer",
		nameAliases: []string{"rhel-edge-simplified-installer"},
		filename:    "simplified-installer.iso",
		mimeType:    "application/x-iso9660-image",
		packageSets: map[string]packageSetFunc{
			// TODO: non-arch-specific package set handling for installers
			// This image type requires build packages for installers and
			// ostree/edge.  For now we only have x86-64 installer build
			// package sets defined.  When we add installer build package sets
			// for other architectures, this will need to be moved to the
			// architecture and the merging will happen in the PackageSets()
			// method like the other sets.
			buildPkgsKey:     edgeSimplifiedInstallerBuildPackageSet,
			installerPkgsKey: edgeSimplifiedInstallerPackageSet,
		},
		defaultImageConfig: &distro.ImageConfig{
			EnabledServices: edgeServices,
		},
		defaultSize:         10 * GigaByte,
		rpmOstree:           true,
		bootable:            true,
		bootISO:             true,
		pipelines:           edgeSimplifiedInstallerPipelines,
		buildPipelines:      []string{"build"},
		payloadPipelines:    []string{"image-tree", "image", "archive", "coi-tree", "efiboot-tree", "bootiso-tree", "bootiso"},
		exports:             []string{"bootiso"},
		basePartitionTables: edgeBasePartitionTables,
	}

	qcow2ImgType := imageType{
		name:          "qcow2",
		filename:      "disk.qcow2",
		mimeType:      "application/x-qemu-disk",
		kernelOptions: "console=tty0 console=ttyS0,115200n8 no_timer_check net.ifnames=0 crashkernel=auto",
		packageSets: map[string]packageSetFunc{
			buildPkgsKey: distroBuildPackageSet,
			osPkgsKey:    qcow2CommonPackageSet,
		},
		defaultImageConfig: &distro.ImageConfig{
			DefaultTarget: "multi-user.target",
			RHSMConfig: map[distro.RHSMSubscriptionStatus]*osbuild.RHSMStageOptions{
				distro.RHSMConfigNoSubscription: {
					DnfPlugins: &osbuild.RHSMStageOptionsDnfPlugins{
						ProductID: &osbuild.RHSMStageOptionsDnfPlugin{
							Enabled: false,
						},
						SubscriptionManager: &osbuild.RHSMStageOptionsDnfPlugin{
							Enabled: false,
						},
					},
				},
			},
		},
		bootable:            true,
		defaultSize:         10 * GigaByte,
		pipelines:           qcow2Pipelines,
		buildPipelines:      []string{"build"},
		payloadPipelines:    []string{"os", "image", "qcow2"},
		exports:             []string{"qcow2"},
		basePartitionTables: defaultBasePartitionTables,
	}

	vhdImgType := imageType{
		name:     "vhd",
		filename: "disk.vhd",
		mimeType: "application/x-vhd",
		packageSets: map[string]packageSetFunc{
			buildPkgsKey: distroBuildPackageSet,
			osPkgsKey:    vhdCommonPackageSet,
		},
		defaultImageConfig: &distro.ImageConfig{
			EnabledServices: []string{
				"sshd",
				"waagent",
			},
			DefaultTarget: "multi-user.target",
		},
		kernelOptions:       "ro biosdevname=0 rootdelay=300 console=ttyS0 earlyprintk=ttyS0 net.ifnames=0",
		bootable:            true,
		defaultSize:         4 * GigaByte,
		pipelines:           vhdPipelines,
		buildPipelines:      []string{"build"},
		payloadPipelines:    []string{"os", "image", "vpc"},
		exports:             []string{"vpc"},
		basePartitionTables: defaultBasePartitionTables,
	}

	azureRhuiImgType := imageType{
		name:     "azure-rhui",
		filename: "disk.vhd",
		mimeType: "application/x-vhd",
		packageSets: map[string]packageSetFunc{
			buildPkgsKey: ec2BuildPackageSet,
			osPkgsKey:    azureRhuiCommonPackageSet,
		},
		defaultImageConfig: &distro.ImageConfig{
			Timezone: "Etc/UTC",
			Locale:   "en_US.UTF-8",
			GPGKeyFiles: []string{
				"/etc/pki/rpm-gpg/RPM-GPG-KEY-microsoft-azure-release",
				"/etc/pki/rpm-gpg/RPM-GPG-KEY-redhat-release",
			},
			Keyboard: &osbuild.KeymapStageOptions{
				Keymap: "us",
				X11Keymap: &osbuild.X11KeymapOptions{
					Layouts: []string{"us"},
				},
			},
			Sysconfig: []*osbuild.SysconfigStageOptions{
				{
					Kernel: &osbuild.SysconfigKernelOptions{
						UpdateDefault: true,
						DefaultKernel: "kernel-core",
					},
					Network: &osbuild.SysconfigNetworkOptions{
						Networking: true,
						NoZeroConf: true,
					},
				},
			},
			EnabledServices: []string{
				"firewalld",
				"sshd",
				"systemd-resolved",
				"waagent",
			},
			SshdConfig: &osbuild.SshdConfigStageOptions{
				Config: osbuild.SshdConfigConfig{
					ClientAliveInterval: common.IntToPtr(180),
				},
			},
			Modprobe: []*osbuild.ModprobeStageOptions{
				{
					Filename: "blacklist-nouveau.conf",
					Commands: osbuild.ModprobeConfigCmdList{
						osbuild.NewModprobeConfigCmdBlacklist("nouveau"),
						osbuild.NewModprobeConfigCmdBlacklist("lbm-nouveau"),
					},
				},
				{
					Filename: "blacklist-floppy.conf",
					Commands: osbuild.ModprobeConfigCmdList{
						osbuild.NewModprobeConfigCmdBlacklist("floppy"),
					},
				},
			},
			CloudInit: []*osbuild.CloudInitStageOptions{
				{
					Filename: "10-azure-kvp.cfg",
					Config: osbuild.CloudInitConfigFile{
						Reporting: &osbuild.CloudInitConfigReporting{
							Logging: &osbuild.CloudInitConfigReportingHandlers{
								Type: "log",
							},
							Telemetry: &osbuild.CloudInitConfigReportingHandlers{
								Type: "hyperv",
							},
						},
					},
				},
				{
					Filename: "91-azure_datasource.cfg",
					Config: osbuild.CloudInitConfigFile{
						Datasource: &osbuild.CloudInitConfigDatasource{
							Azure: &osbuild.CloudInitConfigDatasourceAzure{
								ApplyNetworkConfig: false,
							},
						},
						DatasourceList: []string{
							"Azure",
						},
					},
				},
			},
			PwQuality: &osbuild.PwqualityConfStageOptions{
				Config: osbuild.PwqualityConfConfig{
					Minlen:   common.IntToPtr(6),
					Minclass: common.IntToPtr(3),
					Dcredit:  common.IntToPtr(0),
					Ucredit:  common.IntToPtr(0),
					Lcredit:  common.IntToPtr(0),
					Ocredit:  common.IntToPtr(0),
				},
			},
			WAAgentConfig: &osbuild.WAAgentConfStageOptions{
				Config: osbuild.WAAgentConfig{
					RDFormat:     common.BoolToPtr(false),
					RDEnableSwap: common.BoolToPtr(false),
				},
			},
			RHSMConfig: map[distro.RHSMSubscriptionStatus]*osbuild.RHSMStageOptions{
				distro.RHSMConfigNoSubscription: {
					DnfPlugins: &osbuild.RHSMStageOptionsDnfPlugins{
						SubscriptionManager: &osbuild.RHSMStageOptionsDnfPlugin{
							Enabled: false,
						},
					},
				},
			},
			Grub2Config: &osbuild.GRUB2Config{
				TerminalInput:  []string{"serial", "console"},
				TerminalOutput: []string{"serial", "console"},
				Serial:         "serial --speed=115200 --unit=0 --word=8 --parity=no --stop=1",
				Timeout:        10,
			},
			DefaultTarget: "multi-user.target",
		},
		kernelOptions:       "ro crashkernel=auto console=tty1 console=ttyS0 earlyprintk=ttyS0 rootdelay=300",
		bootable:            true,
		defaultSize:         68719476736,
		pipelines:           vhdPipelines,
		buildPipelines:      []string{"build"},
		payloadPipelines:    []string{"os", "image", "vpc"},
		exports:             []string{"vpc"},
		basePartitionTables: azureRhuiBasePartitionTables,
	}

	vmdkImgType := imageType{
		name:     "vmdk",
		filename: "disk.vmdk",
		mimeType: "application/x-vmdk",
		packageSets: map[string]packageSetFunc{
			buildPkgsKey: distroBuildPackageSet,
			osPkgsKey:    vmdkCommonPackageSet,
		},
		kernelOptions:       "ro net.ifnames=0",
		bootable:            true,
		defaultSize:         4 * GigaByte,
		pipelines:           vmdkPipelines,
		buildPipelines:      []string{"build"},
		payloadPipelines:    []string{"os", "image", "vmdk"},
		exports:             []string{"vmdk"},
		basePartitionTables: defaultBasePartitionTables,
	}

	openstackImgType := imageType{
		name:     "openstack",
		filename: "disk.qcow2",
		mimeType: "application/x-qemu-disk",
		packageSets: map[string]packageSetFunc{
			buildPkgsKey: distroBuildPackageSet,
			osPkgsKey:    openstackCommonPackageSet,
		},
		kernelOptions:       "ro net.ifnames=0",
		bootable:            true,
		defaultSize:         4 * GigaByte,
		pipelines:           openstackPipelines,
		buildPipelines:      []string{"build"},
		payloadPipelines:    []string{"os", "image", "qcow2"},
		exports:             []string{"qcow2"},
		basePartitionTables: defaultBasePartitionTables,
	}

	// default EC2 images config (common for all architectures)
	defaultEc2ImageConfig := &distro.ImageConfig{
		Timezone: "UTC",
		TimeSynchronization: &osbuild.ChronyStageOptions{
			Servers: []osbuild.ChronyConfigServer{
				{
					Hostname: "169.254.169.123",
					Prefer:   common.BoolToPtr(true),
					Iburst:   common.BoolToPtr(true),
					Minpoll:  common.IntToPtr(4),
					Maxpoll:  common.IntToPtr(4),
				},
			},
			// empty string will remove any occurrences of the option from the configuration
			LeapsecTz: common.StringToPtr(""),
		},
		Keyboard: &osbuild.KeymapStageOptions{
			Keymap: "us",
			X11Keymap: &osbuild.X11KeymapOptions{
				Layouts: []string{"us"},
			},
		},
		EnabledServices: []string{
			"sshd",
			"NetworkManager",
			"nm-cloud-setup.service",
			"nm-cloud-setup.timer",
			"cloud-init",
			"cloud-init-local",
			"cloud-config",
			"cloud-final",
			"reboot.target",
		},
		DefaultTarget: "multi-user.target",
		Sysconfig: []*osbuild.SysconfigStageOptions{
			{
				Kernel: &osbuild.SysconfigKernelOptions{
					UpdateDefault: true,
					DefaultKernel: "kernel",
				},
				Network: &osbuild.SysconfigNetworkOptions{
					Networking: true,
					NoZeroConf: true,
				},
				NetworkScripts: &osbuild.NetworkScriptsOptions{
					IfcfgFiles: map[string]osbuild.IfcfgFile{
						"eth0": {
							Device:    "eth0",
							Bootproto: osbuild.IfcfgBootprotoDHCP,
							OnBoot:    common.BoolToPtr(true),
							Type:      osbuild.IfcfgTypeEthernet,
							UserCtl:   common.BoolToPtr(true),
							PeerDNS:   common.BoolToPtr(true),
							IPv6Init:  common.BoolToPtr(false),
						},
					},
				},
			},
		},
		RHSMConfig: map[distro.RHSMSubscriptionStatus]*osbuild.RHSMStageOptions{
			distro.RHSMConfigNoSubscription: {
				// RHBZ#1932802
				SubMan: &osbuild.RHSMStageOptionsSubMan{
					Rhsmcertd: &osbuild.SubManConfigRHSMCERTDSection{
						AutoRegistration: common.BoolToPtr(true),
					},
					Rhsm: &osbuild.SubManConfigRHSMSection{
						ManageRepos: common.BoolToPtr(false),
					},
				},
			},
			distro.RHSMConfigWithSubscription: {
				// RHBZ#1932802
				SubMan: &osbuild.RHSMStageOptionsSubMan{
					Rhsmcertd: &osbuild.SubManConfigRHSMCERTDSection{
						AutoRegistration: common.BoolToPtr(true),
					},
					// do not disable the redhat.repo management if the user
					// explicitly request the system to be subscribed
				},
			},
		},
		SystemdLogind: []*osbuild.SystemdLogindStageOptions{
			{
				Filename: "00-getty-fixes.conf",
				Config: osbuild.SystemdLogindConfigDropin{

					Login: osbuild.SystemdLogindConfigLoginSection{
						NAutoVTs: common.IntToPtr(0),
					},
				},
			},
		},
		CloudInit: []*osbuild.CloudInitStageOptions{
			{
				Filename: "00-rhel-default-user.cfg",
				Config: osbuild.CloudInitConfigFile{
					SystemInfo: &osbuild.CloudInitConfigSystemInfo{
						DefaultUser: &osbuild.CloudInitConfigDefaultUser{
							Name: "ec2-user",
						},
					},
				},
			},
		},
		Modprobe: []*osbuild.ModprobeStageOptions{
			{
				Filename: "blacklist-nouveau.conf",
				Commands: osbuild.ModprobeConfigCmdList{
					osbuild.NewModprobeConfigCmdBlacklist("nouveau"),
				},
			},
		},
		DracutConf: []*osbuild.DracutConfStageOptions{
			{
				Filename: "sgdisk.conf",
				Config: osbuild.DracutConfigFile{
					Install: []string{"sgdisk"},
				},
			},
		},
		SystemdUnit: []*osbuild.SystemdUnitStageOptions{
			// RHBZ#1822863
			{
				Unit:   "nm-cloud-setup.service",
				Dropin: "10-rh-enable-for-ec2.conf",
				Config: osbuild.SystemdServiceUnitDropin{
					Service: &osbuild.SystemdUnitServiceSection{
						Environment: "NM_CLOUD_SETUP_EC2=yes",
					},
				},
			},
		},
		Authselect: &osbuild.AuthselectStageOptions{
			Profile: "sssd",
		},
		SshdConfig: &osbuild.SshdConfigStageOptions{
			Config: osbuild.SshdConfigConfig{
				PasswordAuthentication: common.BoolToPtr(false),
			},
		},
	}

	// default EC2 images config (x86_64)
	defaultEc2ImageConfigX86_64 := &distro.ImageConfig{
		DracutConf: append(defaultEc2ImageConfig.DracutConf,
			&osbuild.DracutConfStageOptions{
				Filename: "ec2.conf",
				Config: osbuild.DracutConfigFile{
					AddDrivers: []string{
						"nvme",
						"xen-blkfront",
					},
				},
			}),
	}
	defaultEc2ImageConfigX86_64 = defaultEc2ImageConfigX86_64.InheritFrom(defaultEc2ImageConfig)

	// default AMI (EC2 BYOS) images config
	defaultAMIImageConfig := &distro.ImageConfig{
		RHSMConfig: map[distro.RHSMSubscriptionStatus]*osbuild.RHSMStageOptions{
			distro.RHSMConfigNoSubscription: {
				// RHBZ#1932802
				SubMan: &osbuild.RHSMStageOptionsSubMan{
					Rhsmcertd: &osbuild.SubManConfigRHSMCERTDSection{
						AutoRegistration: common.BoolToPtr(true),
					},
					// Don't disable RHSM redhat.repo management on the AMI
					// image, which is BYOS and does not use RHUI for content.
					// Otherwise subscribing the system manually after booting
					// it would result in empty redhat.repo. Without RHUI, such
					// system would have no way to get Red Hat content, but
					// enable the repo management manually, which would be very
					// confusing.
				},
			},
			distro.RHSMConfigWithSubscription: {
				// RHBZ#1932802
				SubMan: &osbuild.RHSMStageOptionsSubMan{
					Rhsmcertd: &osbuild.SubManConfigRHSMCERTDSection{
						AutoRegistration: common.BoolToPtr(true),
					},
					// do not disable the redhat.repo management if the user
					// explicitly request the system to be subscribed
				},
			},
		},
	}
	defaultAMIImageConfigX86_64 := defaultAMIImageConfig.InheritFrom(defaultEc2ImageConfigX86_64)
	defaultAMIImageConfig = defaultAMIImageConfig.InheritFrom(defaultEc2ImageConfig)

	amiImgTypeX86_64 := imageType{
		name:     "ami",
		filename: "image.raw",
		mimeType: "application/octet-stream",
		packageSets: map[string]packageSetFunc{
			buildPkgsKey: ec2BuildPackageSet,
			osPkgsKey:    ec2CommonPackageSet,
		},
		defaultImageConfig:  defaultAMIImageConfigX86_64,
		kernelOptions:       "console=ttyS0,115200n8 console=tty0 net.ifnames=0 rd.blacklist=nouveau nvme_core.io_timeout=4294967295 crashkernel=auto",
		bootable:            true,
		bootType:            distro.LegacyBootType,
		defaultSize:         10 * GigaByte,
		pipelines:           ec2Pipelines,
		buildPipelines:      []string{"build"},
		payloadPipelines:    []string{"os", "image"},
		exports:             []string{"image"},
		basePartitionTables: ec2BasePartitionTables,
	}

	amiImgTypeAarch64 := imageType{
		name:     "ami",
		filename: "image.raw",
		mimeType: "application/octet-stream",
		packageSets: map[string]packageSetFunc{
			buildPkgsKey: ec2BuildPackageSet,
			osPkgsKey:    ec2CommonPackageSet,
		},
		defaultImageConfig:  defaultAMIImageConfig,
		kernelOptions:       "console=ttyS0,115200n8 console=tty0 net.ifnames=0 rd.blacklist=nouveau nvme_core.io_timeout=4294967295 iommu.strict=0 crashkernel=auto",
		bootable:            true,
		defaultSize:         10 * GigaByte,
		pipelines:           ec2Pipelines,
		buildPipelines:      []string{"build"},
		payloadPipelines:    []string{"os", "image"},
		exports:             []string{"image"},
		basePartitionTables: ec2BasePartitionTables,
	}

	ec2ImgTypeX86_64 := imageType{
		name:     "ec2",
		filename: "image.raw.xz",
		mimeType: "application/xz",
		packageSets: map[string]packageSetFunc{
			buildPkgsKey: ec2BuildPackageSet,
			osPkgsKey:    rhelEc2PackageSet,
		},
		defaultImageConfig:  defaultEc2ImageConfigX86_64,
		kernelOptions:       "console=ttyS0,115200n8 console=tty0 net.ifnames=0 rd.blacklist=nouveau nvme_core.io_timeout=4294967295 crashkernel=auto",
		bootable:            true,
		bootType:            distro.LegacyBootType,
		defaultSize:         10 * GigaByte,
		pipelines:           rhelEc2Pipelines,
		buildPipelines:      []string{"build"},
		payloadPipelines:    []string{"os", "image", "archive"},
		exports:             []string{"archive"},
		basePartitionTables: ec2BasePartitionTables,
	}

	ec2ImgTypeAarch64 := imageType{
		name:     "ec2",
		filename: "image.raw.xz",
		mimeType: "application/xz",
		packageSets: map[string]packageSetFunc{
			buildPkgsKey: ec2BuildPackageSet,
			osPkgsKey:    rhelEc2PackageSet,
		},
		defaultImageConfig:  defaultEc2ImageConfig,
		kernelOptions:       "console=ttyS0,115200n8 console=tty0 net.ifnames=0 rd.blacklist=nouveau nvme_core.io_timeout=4294967295 iommu.strict=0 crashkernel=auto",
		bootable:            true,
		defaultSize:         10 * GigaByte,
		pipelines:           rhelEc2Pipelines,
		buildPipelines:      []string{"build"},
		payloadPipelines:    []string{"os", "image", "archive"},
		exports:             []string{"archive"},
		basePartitionTables: ec2BasePartitionTables,
	}

	ec2HaImgTypeX86_64 := imageType{
		name:     "ec2-ha",
		filename: "image.raw.xz",
		mimeType: "application/xz",
		packageSets: map[string]packageSetFunc{
			buildPkgsKey: ec2BuildPackageSet,
			osPkgsKey:    rhelEc2HaPackageSet,
		},
		defaultImageConfig:  defaultEc2ImageConfigX86_64,
		kernelOptions:       "console=ttyS0,115200n8 console=tty0 net.ifnames=0 rd.blacklist=nouveau nvme_core.io_timeout=4294967295 crashkernel=auto",
		bootable:            true,
		bootType:            distro.LegacyBootType,
		defaultSize:         10 * GigaByte,
		pipelines:           rhelEc2Pipelines,
		buildPipelines:      []string{"build"},
		payloadPipelines:    []string{"os", "image", "archive"},
		exports:             []string{"archive"},
		basePartitionTables: ec2BasePartitionTables,
	}

	// default EC2-SAP image config (x86_64)
	defaultEc2SapImageConfigX86_64 := &distro.ImageConfig{
		SELinuxConfig: &osbuild.SELinuxConfigStageOptions{
			State: osbuild.SELinuxStatePermissive,
		},
		// RHBZ#1960617
		Tuned: osbuild.NewTunedStageOptions("sap-hana"),
		// RHBZ#1959979
		Tmpfilesd: []*osbuild.TmpfilesdStageOptions{
			osbuild.NewTmpfilesdStageOptions("sap.conf",
				[]osbuild.TmpfilesdConfigLine{
					{
						Type: "x",
						Path: "/tmp/.sap*",
					},
					{
						Type: "x",
						Path: "/tmp/.hdb*lock",
					},
					{
						Type: "x",
						Path: "/tmp/.trex*lock",
					},
				},
			),
		},
		// RHBZ#1959963
		PamLimitsConf: []*osbuild.PamLimitsConfStageOptions{
			osbuild.NewPamLimitsConfStageOptions("99-sap.conf",
				[]osbuild.PamLimitsConfigLine{
					{
						Domain: "@sapsys",
						Type:   osbuild.PamLimitsTypeHard,
						Item:   osbuild.PamLimitsItemNofile,
						Value:  osbuild.PamLimitsValueInt(65536),
					},
					{
						Domain: "@sapsys",
						Type:   osbuild.PamLimitsTypeSoft,
						Item:   osbuild.PamLimitsItemNofile,
						Value:  osbuild.PamLimitsValueInt(65536),
					},
					{
						Domain: "@dba",
						Type:   osbuild.PamLimitsTypeHard,
						Item:   osbuild.PamLimitsItemNofile,
						Value:  osbuild.PamLimitsValueInt(65536),
					},
					{
						Domain: "@dba",
						Type:   osbuild.PamLimitsTypeSoft,
						Item:   osbuild.PamLimitsItemNofile,
						Value:  osbuild.PamLimitsValueInt(65536),
					},
					{
						Domain: "@sapsys",
						Type:   osbuild.PamLimitsTypeHard,
						Item:   osbuild.PamLimitsItemNproc,
						Value:  osbuild.PamLimitsValueUnlimited,
					},
					{
						Domain: "@sapsys",
						Type:   osbuild.PamLimitsTypeSoft,
						Item:   osbuild.PamLimitsItemNproc,
						Value:  osbuild.PamLimitsValueUnlimited,
					},
					{
						Domain: "@dba",
						Type:   osbuild.PamLimitsTypeHard,
						Item:   osbuild.PamLimitsItemNproc,
						Value:  osbuild.PamLimitsValueUnlimited,
					},
					{
						Domain: "@dba",
						Type:   osbuild.PamLimitsTypeSoft,
						Item:   osbuild.PamLimitsItemNproc,
						Value:  osbuild.PamLimitsValueUnlimited,
					},
				},
			),
		},
		// RHBZ#1959962
		Sysctld: []*osbuild.SysctldStageOptions{
			osbuild.NewSysctldStageOptions("sap.conf",
				[]osbuild.SysctldConfigLine{
					{
						Key:   "kernel.pid_max",
						Value: "4194304",
					},
					{
						Key:   "vm.max_map_count",
						Value: "2147483647",
					},
				},
			),
		},
		// E4S/EUS
		DNFConfig: []*osbuild.DNFConfigStageOptions{
			osbuild.NewDNFConfigStageOptions(
				[]osbuild.DNFVariable{
					{
						Name:  "releasever",
						Value: rd.osVersion,
					},
				},
				nil,
			),
		},
	}
	defaultEc2SapImageConfigX86_64 = defaultEc2SapImageConfigX86_64.InheritFrom(defaultEc2ImageConfigX86_64)

	ec2SapImgTypeX86_64 := imageType{
		name:     "ec2-sap",
		filename: "image.raw.xz",
		mimeType: "application/xz",
		packageSets: map[string]packageSetFunc{
			buildPkgsKey: ec2BuildPackageSet,
			osPkgsKey:    rhelEc2SapPackageSet,
		},
		defaultImageConfig:  defaultEc2SapImageConfigX86_64,
		kernelOptions:       "console=ttyS0,115200n8 console=tty0 net.ifnames=0 rd.blacklist=nouveau nvme_core.io_timeout=4294967295 crashkernel=auto processor.max_cstate=1 intel_idle.max_cstate=1",
		bootable:            true,
		bootType:            distro.LegacyBootType,
		defaultSize:         10 * GigaByte,
		pipelines:           rhelEc2Pipelines,
		buildPipelines:      []string{"build"},
		payloadPipelines:    []string{"os", "image", "archive"},
		exports:             []string{"archive"},
		basePartitionTables: ec2BasePartitionTables,
	}

	tarImgType := imageType{
		name:     "tar",
		filename: "root.tar.xz",
		mimeType: "application/x-tar",
		packageSets: map[string]packageSetFunc{
			buildPkgsKey: distroBuildPackageSet,
			osPkgsKey: func(t *imageType) rpmmd.PackageSet {
				return rpmmd.PackageSet{
					Include: []string{"policycoreutils", "selinux-policy-targeted"},
					Exclude: []string{"rng-tools"},
				}
			},
		},
		pipelines:        tarPipelines,
		buildPipelines:   []string{"build"},
		payloadPipelines: []string{"os", "root-tar"},
		exports:          []string{"root-tar"},
	}
	imageInstaller := imageType{
		name:     "image-installer",
		filename: "installer.iso",
		mimeType: "application/x-iso9660-image",
		packageSets: map[string]packageSetFunc{
			buildPkgsKey:     anacondaBuildPackageSet,
			osPkgsKey:        bareMetalPackageSet,
			installerPkgsKey: anacondaPackageSet,
		},
		rpmOstree:        false,
		bootISO:          true,
		bootable:         true,
		pipelines:        imageInstallerPipelines,
		buildPipelines:   []string{"build"},
		payloadPipelines: []string{"os", "anaconda-tree", "bootiso-tree", "bootiso"},
		exports:          []string{"bootiso"},
	}

	ociImgType := qcow2ImgType
	ociImgType.name = "oci"

	x86_64.addImageTypes(qcow2ImgType, vhdImgType, vmdkImgType, openstackImgType, amiImgTypeX86_64, tarImgType, imageInstaller, edgeCommitImgType, edgeInstallerImgType, edgeOCIImgType, edgeRawImgType, edgeSimplifiedInstallerImgType, ociImgType)
	aarch64.addImageTypes(qcow2ImgType, openstackImgType, amiImgTypeAarch64, tarImgType, imageInstaller, edgeCommitImgType, edgeInstallerImgType, edgeOCIImgType, edgeRawImgType, edgeSimplifiedInstallerImgType)
	ppc64le.addImageTypes(qcow2ImgType, tarImgType)
	s390x.addImageTypes(qcow2ImgType, tarImgType)

	if rd.isRHEL() {
		// add azure to RHEL distro only
		x86_64.addImageTypes(azureRhuiImgType)

		// add ec2 image types to RHEL distro only
		x86_64.addImageTypes(ec2ImgTypeX86_64, ec2HaImgTypeX86_64, ec2SapImgTypeX86_64)
		aarch64.addImageTypes(ec2ImgTypeAarch64)

		// add s390x to RHEL distro only
		rd.addArches(s390x)
	}
	rd.addArches(x86_64, aarch64, ppc64le)
	return &rd
}
