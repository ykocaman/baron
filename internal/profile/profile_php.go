package profile

import "path/filepath"

// PHP returns the PHP gate profile for dir, detecting the formatter and
// static analyzer from common config files. There's no standard PHP build
// step (no compilation), so unlike Go/Rust the profile ends at test.
func PHP(dir string) Profile {
	return Profile{
		Name: "php",
		Steps: buildSteps([][]stepAlt{
			{step("install", "composer", []string{"install", "--no-interaction", "--prefer-dist"}, nil)},
			{
				step("format", "php-cs-fixer", []string{"fix", "--dry-run", "--diff"}, func() bool { return hasGlob(dir, ".php-cs-fixer*") }),
				step("format", "phpcs", nil, func() bool { return hasGlob(dir, "phpcs.xml*") }),
			},
			// lint: phpstan if configured, else psalm — both are static
			// analyzers, PHP's rough equivalent of a typechecker, so only
			// one is needed.
			{
				step("lint", "phpstan", []string{"analyse"}, func() bool { return hasGlob(dir, "phpstan*.neon*") }),
				step("lint", "psalm", nil, func() bool { return hasGlob(dir, "psalm.xml*") }),
			},
			{step("test", "phpunit", nil, func() bool { return hasGlob(dir, "phpunit.xml*") })},
		}),
	}
}

// detectPHP reports whether dir is a PHP project (a composer.json manifest).
func detectPHP(dir string) bool {
	return exists(filepath.Join(dir, "composer.json"))
}
