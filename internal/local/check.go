package local

import (
	"fmt"
	"os"
	"path"

	"github.com/google/osv-scanner/pkg/lockfile"
	"github.com/google/osv-scanner/pkg/models"
	"github.com/google/osv-scanner/pkg/osv"
	"github.com/google/osv-scanner/pkg/reporter"
)

const zippedDBRemoteHost = "https://osv-vulnerabilities.storage.googleapis.com"
const envKeyLocalDBCacheDirectory = "OSV_SCANNER_LOCAL_DB_CACHE_DIRECTORY"

func loadDB(dbBasePath string, ecosystem lockfile.Ecosystem, offline bool) (*ZipDB, error) {
	return NewZippedDB(dbBasePath, string(ecosystem), fmt.Sprintf("%s/%s/all.zip", zippedDBRemoteHost, ecosystem), offline)
}

func toPackageDetails(query *osv.Query) (lockfile.PackageDetails, error) {
	if query.Package.PURL != "" {
		pkg, err := models.PURLToPackage(query.Package.PURL)

		if err != nil {
			return lockfile.PackageDetails{}, err
		}

		return lockfile.PackageDetails{
			Name:      pkg.Name,
			Version:   pkg.Version,
			Ecosystem: lockfile.Ecosystem(pkg.Ecosystem),
			CompareAs: lockfile.Ecosystem(pkg.Ecosystem),
		}, nil
	}

	return lockfile.PackageDetails{
		Name:      query.Package.Name,
		Version:   query.Version,
		Commit:    query.Commit,
		Ecosystem: query.Package.Ecosystem,
		CompareAs: query.Package.Ecosystem,
	}, nil
}

// setupLocalDBDirectory attempts to set up the directory the scanner should
// use to store local databases.
//
// if a local path is explicitly provided either by the localDBPath parameter
// or via the envKeyLocalDBCacheDirectory environment variable, the scanner will
// attempt to use the user cache directory if possible or otherwise the temp directory
//
// if an error occurs at any point when a local path is not explicitly provided,
// the scanner will fall back to the temp directory first before finally erroring
func setupLocalDBDirectory(localDBPath string) (string, error) {
	var err error

	// fallback to the env variable if a local database path has not been provided
	if localDBPath == "" {
		if p, envSet := os.LookupEnv(envKeyLocalDBCacheDirectory); envSet {
			localDBPath = p
		}
	}

	implicitPath := localDBPath == ""

	// if we're implicitly picking a path, use the user cache directory if available
	if implicitPath {
		localDBPath, err = os.UserCacheDir()

		if err != nil {
			localDBPath = os.TempDir()
		}
	}

	err = os.Mkdir(path.Join(localDBPath, "osv-scanner"), 0750)

	if err == nil {
		return path.Join(localDBPath, "osv-scanner"), nil
	}

	// if we're implicitly picking a path, try the temp directory before giving up
	if implicitPath && localDBPath != os.TempDir() {
		return setupLocalDBDirectory(os.TempDir())
	}

	return "", err
}

func MakeRequest(r reporter.Reporter, query osv.BatchedQuery, offline bool, localDBPath string) (*osv.HydratedBatchedResponse, error) {
	results := make([]osv.Response, 0, len(query.Queries))
	dbs := make(map[lockfile.Ecosystem]*ZipDB)

	dbBasePath, err := setupLocalDBDirectory(localDBPath)

	if err != nil {
		return &osv.HydratedBatchedResponse{}, fmt.Errorf("could not create %s: %w", dbBasePath, err)
	}

	loadDBFromCache := func(ecosystem lockfile.Ecosystem) (*ZipDB, error) {
		if db, ok := dbs[ecosystem]; ok {
			return db, nil
		}

		db, err := loadDB(dbBasePath, ecosystem, offline)

		if err != nil {
			return nil, err
		}

		r.PrintText(fmt.Sprintf("Loaded %s local db from %s\n", db.Name, db.StoredAt))

		dbs[ecosystem] = db

		return db, nil
	}

	for _, query := range query.Queries {
		pkg, err := toPackageDetails(query)

		if err != nil {
			// currently, this will actually only error if the PURL cannot be parses
			r.PrintError(fmt.Sprintf("skipping %s as it is not a valid PURL: %v\n", query.Package.PURL, err))
			results = append(results, osv.Response{Vulns: []models.Vulnerability{}})

			continue
		}

		db, err := loadDBFromCache(pkg.Ecosystem)

		if err != nil {
			// currently, this will actually only error if the PURL cannot be parses
			r.PrintError(fmt.Sprintf("could not load db for %s ecosystem: %v\n", pkg.Ecosystem, err))
			results = append(results, osv.Response{Vulns: []models.Vulnerability{}})

			continue
		}

		results = append(results, osv.Response{Vulns: db.VulnerabilitiesAffectingPackage(pkg)})
	}

	return &osv.HydratedBatchedResponse{Results: results}, nil
}
