# e2e results — 2026-10-27

Installed with Laranest's own tooling (no system PHP/Composer/Node) and served over HTTPS with the local CA. See `run-projects.sh`.

| Kind | Project | PHP · Laravel · DB | HTTP | Running processes |
|---|---|---|---|---|
| general | [laravel/laravel](https://github.com/laravel/laravel) | 8.5 · 13 · sqlite | 200 | web, queue |
| general | [coollabsio/coolify](https://github.com/coollabsio/coolify) | 8.5 · 12 · pgsql | 200 | web, scheduler |
| general | [bagisto/bagisto](https://github.com/bagisto/bagisto) | 8.4 · 13 · mysql | 200 | web, scheduler |
| general | [monicahq/monica](https://github.com/monicahq/monica) | 8.4 · 12 · sqlite | 200 | web, scheduler |
| general | [krayin/laravel-crm](https://github.com/krayin/laravel-crm) | 8.4 · 12 · mysql | 200 | web, scheduler |
| general | [BookStackApp/BookStack](https://github.com/BookStackApp/BookStack) | 8.5 · 13 · mysql | 200 | web |
| general | [koel/koel](https://github.com/koel/koel) | 8.5 · 13 · sqlite | 200 | web, scheduler |
| general | [pixelfed/pixelfed](https://github.com/pixelfed/pixelfed) | 8.5 · 13 · pgsql | 200 | web, scheduler |
| general | [pterodactyl/panel](https://github.com/pterodactyl/panel) | 8.5 · 12 · mysql | 200 | web, queue, scheduler |
| general | [crater-invoice-inc/crater](https://github.com/crater-invoice-inc/crater) | 8.1 · 8 · sqlite | 200 | web, scheduler |
| general | [firefly-iii/firefly-iii](https://github.com/firefly-iii/firefly-iii) | 8.5 · 13 · sqlite | 200 | web |
| general | [akaunting/akaunting](https://github.com/akaunting/akaunting) | 8.3 · 10 · sqlite | 200 | web, scheduler |
| edge | [cydrobolt/polr](https://github.com/cydrobolt/polr) | — | — | rejected: does not look like a Laravel project (no artisan or laravel/framework) |
| edge | [flarum/flarum](https://github.com/flarum/flarum) | — | — | rejected: does not look like a Laravel project (no artisan or laravel/framework) |
| edge | [freescout-help-desk/freescout](https://github.com/freescout-help-desk/freescout) | — | — | rejected: this project needs PHP >=7.1.0, ≤ 7.4 and Laranest offers 8.0 to 8.5: no compatible version |
| filament | [filamentphp/demo](https://github.com/filamentphp/demo) | 8.5 · 12 · sqlite | 200 | web, scheduler |
| filament | [aureuserp/aureuserp](https://github.com/aureuserp/aureuserp) | 8.4 · 13 · mysql | 200 | web, queue, scheduler |
| filament | [alexjustesen/speedtest-tracker](https://github.com/alexjustesen/speedtest-tracker) | 8.5 · 13 · sqlite | 200 | web, queue, scheduler |
| filament | [solidtime-io/solidtime](https://github.com/solidtime-io/solidtime) | 8.3 · 13 · pgsql | 200 | web, scheduler |
| filament | [relaticle/relaticle](https://github.com/relaticle/relaticle) | 8.5 · 13 · pgsql | 200 | web, queue, scheduler, reverb |
| filament | [laravelio/laravel.io](https://github.com/laravelio/laravel.io) | 8.5 · 13 · mysql | 200 | web, scheduler |

Notes:

- BookStack ships an invalid `APP_KEY` in `.env.example`; the run applies the one-click fix Laranest offers.
- Crater's own front-end build is broken upstream (no lockfile: a newer `pinia` needs a newer `vue-demi`); the app still serves its installer.
- The usual remaining warning is `storage_link` (`php artisan storage:link` not run), which is expected for fresh clones.

## Windows — 2026-10-27

Windows 11 (25H2), with `run-projects.ps1`: the same projects, installed with
`laranest composer`/`npm` (PHP from windows.php.net, no PHP or Composer on the
machine) and served over HTTPS by php-cgi workers. Databases: the e2e
containers in WSL, reached through Windows' localhost.

| Kind | Project | PHP · Laravel | HTTP | Running processes |
|---|---|---|---|---|
| general | laravel/laravel | 8.5 · 13 | 200 | web, queue |
| general | coollabsio/coolify | 8.5 · 12 | 200 | web |
| general | bagisto/bagisto | 8.4 · 13 | 200 | web, scheduler |
| general | monicahq/monica | 8.4 · 12 | 200 | web, scheduler |
| general | krayin/laravel-crm | 8.4 · 12 | 200 | web, scheduler |
| general | BookStackApp/BookStack | 8.5 · 13 | 200 | web |
| general | koel/koel | 8.5 · 13 | 200 | web, scheduler |
| general | pixelfed/pixelfed | 8.5 · 13 | 200 | web, scheduler |
| general | pterodactyl/panel | 8.5 · 12 | 200 | web, queue, scheduler |
| general | crater-invoice-inc/crater | 8.1 · 8 | 200 | web, scheduler |
| general | akaunting/akaunting | 8.3 · 10 | 200 | web, scheduler |
| general | firefly-iii/firefly-iii | 8.5 · 13 | 500 | web — assets: its `postinstall` breaks with `npm --prefix` on Windows |
| filament | filamentphp/demo | 8.5 · 12 | 200 | web, scheduler |
| filament | aureuserp/aureuserp | 8.4 · 13 | 200 | web, queue, scheduler |
| filament | alexjustesen/speedtest-tracker | 8.5 · 13 | 200 | web, queue, scheduler |
| filament | solidtime-io/solidtime | 8.3 · 13 | 200 | web, scheduler |
| filament | relaticle/relaticle | 8.5 · 13 | 500 | web, queue, scheduler, reverb — assets: its `prebuild` runs `rm` |
| filament | laravelio/laravel.io | 8.5 · 13 | 200 | web, scheduler |
| edge | cydrobolt/polr | — | — | rejected: not a Laravel project |
| edge | flarum/flarum | — | — | rejected: not a Laravel project |
| edge | freescout-help-desk/freescout | 7.4 · 5 | — | runs: windows.php.net still offers PHP 7.4 |

Notes:

- Found and fixed while running them: php-cgi workers sharing OPcache's
  memory crashed at random ("Opcode handlers are unusable due to ASLR"); PHP
  couldn't create keys without `OPENSSL_CONF`; Composer scripts that call plain
  `php` (Akaunting) found no PHP on Windows.
- Projects that need Horizon (Coolify, Pixelfed, Relaticle) install with
  `--ignore-platform-req=ext-pcntl --ignore-platform-req=ext-posix`, as the
  panel suggests on Windows.
- Firefly III and Relaticle are served once their front end is built some
  other way: their npm scripts assume a Unix shell.
