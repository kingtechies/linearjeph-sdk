<?php
declare(strict_types=1);

namespace Linearjeph\Prophecy;

/**
 * Local codebase scanner. Reads files on this machine and returns a manifest (schema 1). It never includes source code,
 * env values or secrets: env NAMES only, secrets_suspected is a count. Skips node_modules, .git, vendor, venv, dist,
 * build; files over 1 MB; more than 20,000 files; symlinks (nothing outside the root is read).
 */
final class Scanner
{
    private const MAX_FILES = 20000;
    private const MAX_BYTES = 1048576;
    private const MAX_DEPS = 2000;
    private const MAX_ROUTES = 3000;
    private const MAX_FEATURES = 150;
    private const MAX_ENV = 500;

    private const SKIP_DIRS = ['node_modules', '.git', 'vendor', 'venv', '.venv', 'dist', 'build', '__pycache__', '.next', '.nuxt', '.cache', 'coverage', '.tox', 'target', '.gradle', '.idea', '.terraform', 'bower_components'];
    private const CODE_EXTS = ['go', 'py', 'js', 'mjs', 'cjs', 'ts', 'tsx', 'jsx', 'php', 'rb', 'java', 'kt', 'kts', 'swift', 'dart', 'rs', 'c', 'h', 'cc', 'cpp', 'cs', 'vue', 'svelte', 'scala', 'ex', 'exs', 'lua', 'sh', 'sql', 'html', 'css', 'scss'];
    private const TEXT_EXTS = ['json', 'yml', 'yaml', 'toml', 'ini', 'cfg', 'conf', 'env', 'txt', 'md', 'mod', 'lock', 'xml', 'tf', 'properties'];
    private const INTEGRATIONS = [['stripe', 'stripe'], ['paddle', 'paddle'], ['paystack', 'paystack'], ['flutterwave', 'flutterwave'], ['bachs', 'bachs'], ['auth0', 'auth0'], ['clerk', 'clerk'], ['supabase', 'supabase'], ['firebase', 'firebase'], ['sentry', 'sentry'], ['segment', 'segment'], ['mixpanel', 'mixpanel'], ['posthog', 'posthog'], ['twilio', 'twilio'], ['sendgrid', 'sendgrid'], ['resend', 'resend'], ['openai', 'openai'], ['anthropic', 'anthropic'], ['aws-sdk', 's3'], ['boto3', 's3'], ['aws_s3', 's3'], ['s3_bucket', 's3'], ['mailgun', 'mailgun'], ['postmark', 'postmark'], ['algolia', 'algolia'], ['cloudinary', 'cloudinary'], ['plaid', 'plaid'], ['shopify', 'shopify'], ['datadog', 'datadog'], ['newrelic', 'newrelic'], ['linearjeph', 'linearjeph'], ['redis', 'redis'], ['kafka', 'kafka'], ['rabbitmq', 'rabbitmq']];
    private const FRAMEWORK_DEPS = [
        'npm:express' => 'express', 'npm:next' => 'next', 'npm:react' => 'react', 'npm:vue' => 'vue', 'npm:nuxt' => 'nuxt',
        'npm:svelte' => 'svelte', 'npm:@nestjs/core' => 'nestjs', 'npm:fastify' => 'fastify', 'npm:koa' => 'koa', 'npm:hono' => 'hono',
        'npm:@angular/core' => 'angular', 'pypi:django' => 'django', 'pypi:flask' => 'flask', 'pypi:fastapi' => 'fastapi',
        'pypi:tornado' => 'tornado', 'pypi:starlette' => 'starlette', 'composer:laravel/framework' => 'laravel',
        'composer:symfony/framework-bundle' => 'symfony', 'composer:slim/slim' => 'slim', 'gem:rails' => 'rails',
        'gem:sinatra' => 'sinatra', 'gem:hanami' => 'hanami', 'gem:roda' => 'roda',
    ];
    private const GO_FRAMEWORKS = ['github.com/gin-gonic/gin' => 'gin', 'github.com/go-chi/chi' => 'chi', 'github.com/labstack/echo' => 'echo', 'github.com/gofiber/fiber' => 'fiber', 'github.com/gorilla/mux' => 'gorilla-mux'];
    private const TEST_DEPS = ['jest' => 'jest', 'vitest' => 'vitest', 'mocha' => 'mocha', '@playwright/test' => 'playwright', 'cypress' => 'cypress', 'pytest' => 'pytest', 'phpunit/phpunit' => 'phpunit', 'pestphp/pest' => 'pest', 'rspec' => 'rspec', 'rspec-rails' => 'rspec', 'minitest' => 'minitest'];
    private const SECRETS = [
        '~AKIA[0-9A-Z]{16}~', '~-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----~', '~sk_live_[0-9A-Za-z]{16,}~',
        '~gh[pousr]_[A-Za-z0-9]{30,}~', '~xox[baprs]-[A-Za-z0-9-]{10,}~', '~ljp_sk_[A-Za-z0-9_-]{20,}~', '~AIza[0-9A-Za-z_-]{35}~',
    ];

    private int $files = 0;
    private int $loc = 0;
    private array $langs = [];
    private array $deps = [];
    private array $depList = [];
    private array $routes = [];
    private array $routeSeen = [];
    private array $env = [];
    private array $dirFiles = [];
    private array $flagSeen = [];
    private array $flags = [];
    private array $ci = [];
    private array $hosting = [];
    private array $testFw = [];
    private array $priceSeen = [];
    private array $pricing = [];
    private array $frameworks = [];
    private array $infra = ['docker' => false, 'compose' => false, 'kubernetes' => false, 'terraform' => false, 'serverless' => false];
    private array $security = ['lockfile' => false, 'env_committed' => false, 'secrets_suspected' => 0];
    private int $testFiles = 0;
    private string $description = '';

    public function scan(string $root): array
    {
        $abs = realpath($root);
        if ($abs === false || !is_dir($abs)) {
            throw new ProphecyException("scan: not a directory: $root");
        }
        $this->walk($abs, '');
        return $this->finish($abs);
    }

    private function walk(string $abs, string $relDir): void
    {
        $dir = $relDir === '' ? $abs : $abs . '/' . $relDir;
        $names = @scandir($dir);
        if ($names === false) {
            return;
        }
        sort($names, SORT_STRING);
        foreach ($names as $name) {
            if ($name === '.' || $name === '..') {
                continue;
            }
            if ($this->files >= self::MAX_FILES) {
                return;
            }
            $full = $dir . '/' . $name;
            $rel = $relDir === '' ? $name : $relDir . '/' . $name;
            if (is_link($full)) {
                continue; // never follow symlinks: no escape from the root
            }
            if (is_dir($full)) {
                if (in_array($name, self::SKIP_DIRS, true)) {
                    continue;
                }
                if (in_array($name, ['k8s', 'kubernetes', 'helm', 'charts'], true)) {
                    $this->infra['kubernetes'] = true;
                }
                if ($rel === '.github/workflows') {
                    $this->ci['github-actions'] = true;
                }
                if ($rel === '.circleci') {
                    $this->ci['circleci'] = true;
                }
                $this->walk($abs, $rel);
                continue;
            }
            if (!is_file($full)) {
                continue;
            }
            $size = @filesize($full);
            if ($size === false || $size > self::MAX_BYTES) {
                continue;
            }
            $this->files++;
            $this->file($full, $rel, $name);
        }
    }

    private function addDep(string $eco, string $name, string $ver): void
    {
        $k = "$eco:$name";
        if ($name === '' || isset($this->deps[$k]) || count($this->depList) >= self::MAX_DEPS) {
            return;
        }
        $this->deps[$k] = true;
        $this->depList[] = ['ecosystem' => $eco, 'name' => $name, 'version' => $ver];
    }

    private function addRoute(string $method, string $path, string $file): void
    {
        if ($path === '' || count($this->routes) >= self::MAX_ROUTES) {
            return;
        }
        $method = strtoupper($method);
        $path = preg_replace('~\{(\w+)[^}]*\}~', ':$1', $path) ?? $path;
        $k = "$method $path $file";
        if (isset($this->routeSeen[$k])) {
            return;
        }
        $this->routeSeen[$k] = true;
        $this->routes[] = ['method' => $method, 'path' => $path, 'file' => $file];
    }

    private function addEnv(string $n): void
    {
        if (count($this->env) < self::MAX_ENV) {
            $this->env[$n] = true;
        }
    }

    private function fileInfra(string $rel, string $base): void
    {
        $lb = strtolower($base);
        if ($lb === 'dockerfile' || str_starts_with($lb, 'dockerfile.') || str_ends_with($lb, '.dockerfile')) {
            $this->infra['docker'] = true;
        } elseif (in_array($lb, ['docker-compose.yml', 'docker-compose.yaml', 'compose.yml', 'compose.yaml'], true) || str_starts_with($lb, 'docker-compose.')) {
            $this->infra['compose'] = true;
        } elseif (str_ends_with($lb, '.tf')) {
            $this->infra['terraform'] = true;
        } elseif (in_array($lb, ['serverless.yml', 'serverless.yaml', 'serverless.ts', 'serverless.js'], true)) {
            $this->infra['serverless'] = true;
        }
        $ci = ['.gitlab-ci.yml' => 'gitlab', 'jenkinsfile' => 'jenkins', 'azure-pipelines.yml' => 'azure-pipelines', 'bitbucket-pipelines.yml' => 'bitbucket', '.travis.yml' => 'travis'];
        if (isset($ci[$lb])) {
            $this->ci[$ci[$lb]] = true;
        }
        $host = ['vercel.json' => 'vercel', 'netlify.toml' => 'netlify', 'fly.toml' => 'fly', 'render.yaml' => 'render', 'render.yml' => 'render', 'procfile' => 'heroku', 'railway.json' => 'railway', 'railway.toml' => 'railway', 'wrangler.toml' => 'cloudflare', 'wrangler.json' => 'cloudflare', 'firebase.json' => 'firebase'];
        if (isset($host[$lb])) {
            $this->hosting[$host[$lb]] = true;
        }
        if ($lb === 'app.yaml' && !str_contains($rel, '/')) {
            $this->hosting['gcp-app-engine'] = true;
        }
        if (str_contains($rel, 'k8s/') || str_contains($rel, 'kubernetes/')) {
            $this->infra['kubernetes'] = true;
        }
    }

    private function isTestFile(string $base): bool
    {
        $lb = strtolower($base);
        foreach (['_test.go', '_test.py', '_spec.rb', '_test.rb', 'test.php', '_test.exs'] as $suf) {
            if (str_ends_with($lb, $suf)) {
                return true;
            }
        }
        return (str_starts_with($lb, 'test_') && str_ends_with($lb, '.py')) || str_contains($lb, '.test.') || str_contains($lb, '.spec.');
    }

    private function file(string $full, string $rel, string $base): void
    {
        $this->fileInfra($rel, $base);
        $ext = strtolower(pathinfo($base, PATHINFO_EXTENSION));
        $lb = strtolower($base);
        $isEnvFile = str_starts_with($lb, '.env') || str_ends_with($lb, '.env');
        if ($isEnvFile && !preg_match('~example|sample|template|dist~', $lb)) {
            $this->security['env_committed'] = true;
        }
        switch ($lb) {
            case 'package-lock.json': case 'yarn.lock': case 'pnpm-lock.yaml': case 'composer.lock': case 'go.sum':
            case 'poetry.lock': case 'pipfile.lock': case 'uv.lock': case 'bun.lockb': case 'gemfile.lock':
                $this->security['lockfile'] = true;
                break;
            case 'artisan':
                $this->frameworks[] = 'laravel';
                break;
            case 'manage.py':
                $this->frameworks[] = 'django';
                break;
            case 'conftest.py':
                $this->testFw['pytest'] = true;
                break;
            case 'phpunit.xml': case 'phpunit.xml.dist':
                $this->testFw['phpunit'] = true;
                break;
        }
        if ($rel === 'config/routes.rb') {
            $this->frameworks[] = 'rails';
        }
        $code = in_array($ext, self::CODE_EXTS, true);
        if (!$code && !in_array($ext, self::TEXT_EXTS, true) && !$isEnvFile && $lb !== 'gemfile' && !str_starts_with($lb, 'readme')) {
            return;
        }
        $data = @file_get_contents($full);
        if ($data === false || str_contains(substr($data, 0, 512), "\0")) {
            return;
        }
        if ($code) {
            $this->langs[$ext] = ($this->langs[$ext] ?? 0) + 1;
            foreach (explode("\n", $data) as $ln) {
                if (trim($ln) !== '') {
                    $this->loc++;
                }
            }
            if ($this->isTestFile($base)) {
                $this->testFiles++;
                if (str_ends_with($lb, '_test.go')) {
                    $this->testFw['go test'] = true;
                } elseif ($ext === 'py') {
                    if (str_contains($data, 'pytest')) {
                        $this->testFw['pytest'] = true;
                    }
                    if (str_contains($data, 'unittest')) {
                        $this->testFw['unittest'] = true;
                    }
                } elseif ($ext === 'rb') {
                    $this->testFw[str_ends_with($lb, '_spec.rb') ? 'rspec' : 'minitest'] = true;
                } elseif ($ext === 'php') {
                    $this->testFw['phpunit'] = true;
                }
            }
            $i = strpos($rel, '/');
            if ($i !== false && $i > 0) {
                $top = substr($rel, 0, $i);
                if (in_array($top, ['src', 'app', 'apps', 'packages', 'internal', 'pkg', 'modules', 'lib', 'cmd', 'services'], true)) {
                    $parts = explode('/', $rel, 3);
                    if (count($parts) === 3) {
                        $k = $top . '/' . $parts[1];
                        $this->dirFiles[$k] = ($this->dirFiles[$k] ?? 0) + 1;
                    }
                } else {
                    $this->dirFiles[$top] = ($this->dirFiles[$top] ?? 0) + 1;
                }
            }
            $this->routesFor($rel, $ext, $data);
            $this->envFromCode($data);
            if (preg_match_all('~(?:isEnabled|is_enabled|isFeatureEnabled|feature_enabled\??|featureEnabled|flagEnabled|Feature::active|useFlag|useFeatureFlag|getBooleanFlag|feature_flag|FeatureFlag)\(\s*[\'"]([\w.:-]{2,60})[\'"]~i', $data, $mm)) {
                foreach ($mm[1] as $n) {
                    $n = strtolower($n);
                    if (!isset($this->flagSeen[$n]) && count($this->flags) < 100) {
                        $this->flagSeen[$n] = true;
                        $this->flags[] = ['name' => $n, 'source' => 'flag', 'paths' => [$rel]];
                    }
                }
            }
        }
        if ($isEnvFile) {
            foreach (explode("\n", $data) as $ln) {
                if (preg_match('~^\s*(?:export\s+)?([A-Z][A-Z0-9_]*)\s*=~', $ln, $m)) {
                    $this->addEnv($m[1]);
                }
            }
        }
        $this->manifestFile($lb, $data);
        if (str_starts_with($lb, 'readme') && !str_contains($rel, '/') && $this->description === '') {
            $this->description = $this->describe($data);
        }
        foreach (self::SECRETS as $re) {
            $n = preg_match_all($re, $data);
            if ($n) {
                $this->security['secrets_suspected'] += $n;
            }
        }
        if (preg_match('~plan|pricing|price|billing|subscription|tier~i', $rel) && count($this->pricing) < 20
            && in_array($ext, ['json', 'js', 'ts', 'py', 'php', 'rb', 'go', 'yml', 'yaml'], true)) {
            $this->pricingHints($data);
        }
    }

    private function envFromCode(string $t): void
    {
        foreach ([
            '~(?:Getenv|LookupEnv|getenv|env|fetch|get|config)\(\s*["\']([A-Z][A-Z0-9_]{1,63})["\']~',
            '~(?:process\.env|import\.meta\.env)\.([A-Z][A-Z0-9_]{1,63})~',
            '~(?:ENV|environ|_ENV|_SERVER)\[\s*["\']([A-Z][A-Z0-9_]{1,63})["\']~',
        ] as $re) {
            if (preg_match_all($re, $t, $m)) {
                foreach ($m[1] as $n) {
                    $this->addEnv($n);
                }
            }
        }
    }

    private function pricingHints(string $text): void
    {
        $plan = '';
        $cur = '';
        foreach (explode("\n", $text) as $ln) {
            if (strlen($ln) > 2000) {
                continue;
            }
            if (preg_match('~\b(USD|EUR|GBP|NGN|GHS|KES|ZAR|CAD|AUD|INR)\b~i', $ln, $m)) {
                $cur = strtoupper($m[1]);
            }
            preg_match_all('~\b(?:name|plan|id|slug|key|tier)\b["\']?\s*(?:=>|[:=])\s*["\']([A-Za-z][\w .-]{0,39})["\']~i', $ln, $pl, PREG_SET_ORDER | PREG_OFFSET_CAPTURE);
            preg_match_all('~\b(?:price|amount|monthly|mrr|cost)\w*["\']?\s*(?:=>|[:=])\s*(\d{1,6}(?:\.\d{1,2})?)\b~i', $ln, $pr, PREG_SET_ORDER | PREG_OFFSET_CAPTURE);
            $pi = 0;
            foreach ($pr as $p) {
                while ($pi < count($pl) && $pl[$pi][0][1] < $p[0][1]) {
                    $plan = trim($pl[$pi][1][0]);
                    $pi++;
                }
                $num = $p[1][0];
                $amt = (float) $num;
                if ($amt <= 0) {
                    continue;
                }
                $name = $plan !== '' ? $plan : 'default';
                $k = $name . '|' . $num;
                if (isset($this->priceSeen[$k]) || count($this->pricing) >= 20) {
                    continue;
                }
                $this->priceSeen[$k] = true;
                $this->pricing[] = ['plan' => $name, 'amount' => $amt, 'currency' => $cur !== '' ? $cur : 'USD'];
            }
            for (; $pi < count($pl); $pi++) {
                $plan = trim($pl[$pi][1][0]);
            }
        }
    }

    private function describe(string $data): string
    {
        $t = substr($data, 0, 2048);
        if (function_exists('iconv')) {
            $t = (string) @iconv('UTF-8', 'UTF-8//IGNORE', $t);
        }
        $t = preg_replace('~!\[[^\]]*\]\([^)]*\)~', '', $t) ?? $t;
        $t = preg_replace('~\[([^\]]*)\]\([^)]*\)~', '$1', $t) ?? $t;
        $t = preg_replace('~<[^>]+>~', '', $t) ?? $t;
        $t = str_replace(['```', '`', '#', '*'], [' ', '', ' ', ''], $t);
        return trim(preg_replace('~\s+~u', ' ', $t) ?? $t);
    }

    private function routesFor(string $rel, string $ext, string $text): void
    {
        switch ($ext) {
            case 'go':
                $prefix = [];
                if (preg_match_all('~(\w+)\s*:?=\s*(\w+)\.(?:Group|Route)\(\s*"([^"]*)"~', $text, $gm, PREG_SET_ORDER)) {
                    foreach ($gm as $g) {
                        $prefix[$g[1]] = ($prefix[$g[2]] ?? '') . $g[3];
                    }
                }
                if (preg_match_all('~\b(\w+)\.(Get|Post|Put|Patch|Delete|Head|Options|GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|Any|Handle|HandleFunc)\(\s*(?:"([^"]+)"|`([^`]+)`)~', $text, $ms, PREG_SET_ORDER)) {
                    foreach ($ms as $m) {
                        $p = ($m[3] ?? '') !== '' ? $m[3] : ($m[4] ?? '');
                        $method = strtoupper($m[2]);
                        if ($method === 'HANDLE' || $method === 'HANDLEFUNC') {
                            $method = 'ANY';
                            $f = preg_split('~\s+~', trim($p));
                            if (count($f) === 2) {
                                [$method, $p] = $f;
                            }
                            $i = strpos($p, '/');
                            if ($i !== false && $i > 0) {
                                $p = substr($p, $i);
                            }
                        }
                        if (!str_starts_with($p, '/')) {
                            continue;
                        }
                        $this->addRoute($method, ($prefix[$m[1]] ?? '') . $p, $rel);
                    }
                }
                break;
            case 'php':
                if (preg_match_all('~Route::(get|post|put|patch|delete|any|resource|apiResource)\(\s*[\'"]([^\'"]+)[\'"]~', $text, $ms, PREG_SET_ORDER)) {
                    foreach ($ms as $m) {
                        $p = '/' . ltrim($m[2], '/');
                        if ($m[1] === 'resource' || $m[1] === 'apiResource') {
                            $this->addRoute('GET', $p, $rel);
                            $this->addRoute('POST', $p, $rel);
                            $this->addRoute('GET', "$p/:id", $rel);
                            $this->addRoute('PUT', "$p/:id", $rel);
                            $this->addRoute('DELETE', "$p/:id", $rel);
                        } else {
                            $this->addRoute($m[1], $p, $rel);
                        }
                    }
                }
                break;
            case 'rb':
                $this->rubyRoutes($rel, $text);
                break;
            case 'py':
                if (str_ends_with($rel, 'urls.py') && preg_match_all('~\b(?:re_)?path\(\s*r?[\'"]([^\'"]*)[\'"]~', $text, $ms)) {
                    foreach ($ms[1] as $p) {
                        $p = preg_replace('~<(?:\w+:)?(\w+)>~', ':$1', $p) ?? $p;
                        $this->addRoute('ANY', '/' . ltrim($p, '/'), $rel);
                    }
                }
                if (preg_match_all('~@\w+\.(route|get|post|put|patch|delete)\(\s*[\'"]([^\'"]+)[\'"]~', $text, $ms, PREG_SET_ORDER)) {
                    foreach ($ms as $m) {
                        if (str_starts_with($m[2], '/')) {
                            $this->addRoute($m[1] === 'route' ? 'ANY' : $m[1], $m[2], $rel);
                        }
                    }
                }
                break;
            case 'js': case 'mjs': case 'cjs': case 'ts': case 'jsx': case 'tsx':
                if (preg_match_all('~\b(?:app|router|server|fastify|api|routes)\.(get|post|put|patch|delete|all)\(\s*[\'"`]([^\'"`]+)[\'"`]~', $text, $ms, PREG_SET_ORDER)) {
                    foreach ($ms as $m) {
                        if (str_starts_with($m[2], '/') || $m[2] === '*') {
                            $this->addRoute($m[1] === 'all' ? 'ANY' : $m[1], $m[2], $rel);
                        }
                    }
                }
                $this->nextRoute($rel, $ext);
                break;
        }
    }

    private function rubyRoutes(string $rel, string $text): void
    {
        if (!str_ends_with($rel, 'routes.rb') && !str_contains($rel, 'routes/')) {
            if (str_contains($text, 'Sinatra') || str_contains($text, 'Roda')) {
                foreach (explode("\n", $text) as $ln) {
                    if (preg_match('~^\s*(get|post|put|patch|delete)\s+[\'"]([^\'"]+)[\'"]~', $ln, $m)) {
                        $this->addRoute($m[1], $m[2], $rel);
                    }
                }
            }
            return;
        }
        $stack = [];
        foreach (explode("\n", $text) as $ln) {
            $ln = rtrim($ln, "\r");
            if (preg_match('~^\s*end\b~', $ln)) {
                array_pop($stack);
                continue;
            }
            $pre = implode('/', array_filter($stack, static fn ($x) => $x !== ''));
            $pre = $pre === '' ? '' : '/' . $pre;
            if (preg_match('~^\s*(?:namespace|scope)\s+:?[\'"]?(\w+)[\'"]?.*\bdo\b~', $ln, $m)) {
                $stack[] = $m[1];
                continue;
            }
            if (preg_match('~^\s*(resources?)\s+:(\w+)~', $ln, $m)) {
                $p = $pre . '/' . $m[2];
                if ($m[1] === 'resources') {
                    foreach ([['GET', $p], ['POST', $p], ['GET', "$p/:id"], ['PUT', "$p/:id"], ['DELETE', "$p/:id"]] as [$mt, $pt]) {
                        $this->addRoute($mt, $pt, $rel);
                    }
                } else {
                    $this->addRoute('GET', $p, $rel);
                }
            } elseif (preg_match('~^\s*(get|post|put|patch|delete)\s+[\'"]([^\'"]+)[\'"]~', $ln, $m)) {
                $this->addRoute($m[1], $pre . '/' . ltrim($m[2], '/'), $rel);
            }
            if (preg_match('~\bdo\s*(\|[^|]*\|)?\s*$~', $ln)) {
                $stack[] = '';
            }
        }
    }

    private function nextRoute(string $rel, string $ext): void
    {
        $r = str_starts_with($rel, 'src/') ? substr($rel, 4) : $rel;
        if (str_starts_with($r, 'app/')) {
            $kind = 'app';
            $sub = substr($r, 4);
        } elseif (str_starts_with($r, 'pages/')) {
            $kind = 'pages';
            $sub = substr($r, 6);
        } else {
            return;
        }
        $segs = explode('/', substr($sub, 0, -(strlen($ext) + 1)));
        $last = array_pop($segs);
        $conv = static function (string $x): string {
            if (str_starts_with($x, '(') && str_ends_with($x, ')')) {
                return '';
            }
            if (str_starts_with($x, '[') && str_ends_with($x, ']')) {
                return ':' . trim($x, '[].');
            }
            return $x;
        };
        $out = [];
        foreach ($segs as $x) {
            if (($c = $conv($x)) !== '') {
                $out[] = $c;
            }
        }
        $method = 'GET';
        if ($kind === 'app') {
            if ($last === 'route') {
                $method = 'ANY';
            } elseif ($last !== 'page') {
                return;
            }
        } else {
            if (str_starts_with($last, '_')) {
                return;
            }
            if ($last !== 'index' && ($c = $conv($last)) !== '') {
                $out[] = $c;
            }
            if ($out && $out[0] === 'api') {
                $method = 'ANY';
            }
        }
        $this->addRoute($method, '/' . implode('/', $out), $rel);
    }

    private function manifestFile(string $lb, string $text): void
    {
        if ($lb === 'go.mod') {
            $in = false;
            foreach (explode("\n", $text) as $ln) {
                $ln = trim($ln);
                if (str_starts_with($ln, 'require (')) {
                    $in = true;
                    continue;
                }
                if ($in && $ln === ')') {
                    $in = false;
                    continue;
                }
                if (!$in) {
                    if (!str_starts_with($ln, 'require ')) {
                        continue;
                    }
                    $ln = trim(substr($ln, 8));
                }
                if (str_contains($ln, '// indirect')) {
                    continue;
                }
                $f = preg_split('~\s+~', $ln);
                if (count($f) >= 2 && !str_starts_with($f[0], '//')) {
                    $this->addDep('go', $f[0], $f[1]);
                }
            }
        } elseif ($lb === 'package.json' || $lb === 'composer.json') {
            $pj = json_decode($text, true);
            if (!is_array($pj)) {
                return;
            }
            $npm = $lb === 'package.json';
            foreach ($npm ? ['dependencies', 'devDependencies'] : ['require', 'require-dev'] as $k) {
                $m = $pj[$k] ?? null;
                if (!is_array($m)) {
                    continue;
                }
                ksort($m, SORT_STRING);
                foreach ($m as $n => $v) {
                    $n = (string) $n;
                    if (!$npm && ($n === 'php' || str_starts_with($n, 'ext-') || str_starts_with($n, 'lib-'))) {
                        continue;
                    }
                    $this->addDep($npm ? 'npm' : 'composer', $n, is_string($v) ? $v : '');
                }
            }
        } elseif ($lb === 'gemfile.lock') {
            $inGem = false;
            foreach (explode("\n", $text) as $ln) {
                $ln = rtrim($ln, "\r");
                if ($ln === '' ) {
                    continue;
                }
                if ($ln[0] !== ' ') {
                    $inGem = str_starts_with($ln, 'GEM');
                    continue;
                }
                if ($inGem && str_starts_with($ln, '    ') && !str_starts_with($ln, '     ')) {
                    $t = trim($ln);
                    $i = strpos($t, ' (');
                    if ($i !== false && $i > 0 && str_ends_with($t, ')')) {
                        $this->addDep('gem', substr($t, 0, $i), substr($t, $i + 2, -1));
                    }
                }
            }
        } elseif ($lb === 'gemfile') {
            foreach (explode("\n", $text) as $ln) {
                if (preg_match('~^\s*gem\s+[\'"]([^\'"]+)[\'"](?:\s*,\s*[\'"]([^\'"]+)[\'"])?~', $ln, $m)) {
                    $this->addDep('gem', $m[1], $m[2] ?? '');
                }
            }
        } elseif (str_starts_with($lb, 'requirements') && str_ends_with($lb, '.txt')) {
            foreach (explode("\n", $text) as $ln) {
                $this->pyReq($ln);
            }
        } elseif ($lb === 'pyproject.toml') {
            $section = '';
            $inArr = false;
            foreach (explode("\n", $text) as $ln) {
                $t = trim($ln);
                if (str_starts_with($t, '[')) {
                    $section = trim($t, '[] ');
                    $inArr = false;
                    continue;
                }
                if ($section === 'project' && str_starts_with($t, 'dependencies') && str_contains($t, '[')) {
                    $inArr = true;
                    $t = substr($t, strpos($t, '[') + 1);
                }
                if ($inArr) {
                    if (preg_match_all('~"([^"]+)"|\'([^\']+)\'~', $t, $qs, PREG_SET_ORDER)) {
                        foreach ($qs as $q) {
                            $this->pyReq($q[1] !== '' ? $q[1] : ($q[2] ?? ''));
                        }
                    }
                    if (str_contains($t, ']')) {
                        $inArr = false;
                    }
                    continue;
                }
                if (in_array($section, ['tool.poetry.dependencies', 'tool.poetry.dev-dependencies', 'tool.poetry.group.dev.dependencies'], true)) {
                    $i = strpos($t, '=');
                    if ($i !== false && $i > 0 && !str_starts_with($t, '#')) {
                        $name = trim(substr($t, 0, $i));
                        if ($name === 'python') {
                            continue;
                        }
                        $ver = trim(trim(substr($t, $i + 1)), '"\'');
                        if (preg_match('~version\s*=\s*["\']([^"\']+)~', $t, $vm)) {
                            $ver = $vm[1];
                        }
                        $this->addDep('pypi', strtolower($name), $ver);
                    }
                }
            }
        }
    }

    private function pyReq(string $ln): void
    {
        $ln = trim($ln);
        if ($ln === '' || $ln[0] === '#' || $ln[0] === '-') {
            return;
        }
        $i = strpos($ln, ';');
        if ($i !== false && $i > 0) {
            $ln = substr($ln, 0, $i);
        }
        if (!preg_match('#^([A-Za-z0-9_.\-]+)(?:\[[^\]]*\])?\s*(?:(==|>=|<=|~=|!=|>|<)\s*([\w.*+-]+))?#', $ln, $m)) {
            return;
        }
        $op = $m[2] ?? '';
        $ver = $m[3] ?? '';
        if ($op !== '' && $op !== '==') {
            $ver = $op . $ver;
        }
        $this->addDep('pypi', strtolower($m[1]), $ver);
    }

    private function finish(string $abs): array
    {
        $fw = array_fill_keys($this->frameworks, true);
        $lowerDeps = [];
        $testFw = $this->testFw;
        foreach ($this->depList as $d) {
            $k = $d['ecosystem'] . ':' . $d['name'];
            if (isset(self::FRAMEWORK_DEPS[$k])) {
                $fw[self::FRAMEWORK_DEPS[$k]] = true;
            }
            if ($d['ecosystem'] === 'go') {
                foreach (self::GO_FRAMEWORKS as $prefix => $n) {
                    if (str_starts_with($d['name'], $prefix)) {
                        $fw[$n] = true;
                    }
                }
            }
            if (isset(self::TEST_DEPS[$d['name']])) {
                $testFw[self::TEST_DEPS[$d['name']]] = true;
            }
            $lowerDeps[] = strtolower($d['name']);
        }
        foreach ($this->routes as $r) {
            if (str_ends_with($r['file'], '.go') && !isset($fw['gin']) && !isset($fw['chi']) && !isset($fw['echo']) && !isset($fw['fiber'])) {
                $fw['net/http'] = true;
            }
        }
        $env = array_keys($this->env);
        sort($env, SORT_STRING);
        $integ = [];
        $hay = array_merge($lowerDeps, array_map('strtolower', $env));
        foreach (self::INTEGRATIONS as [$kw, $name]) {
            foreach ($hay as $h) {
                if (str_contains($h, $kw)) {
                    $integ[$name] = true;
                    break;
                }
            }
        }
        // features
        $groups = [];
        foreach ($this->routes as $r) {
            $n = $this->routeGroup($r['path']);
            if ($n === '') {
                continue;
            }
            if (!isset($groups[$n])) {
                $groups[$n] = [];
            }
            if (count($groups[$n]) < 5) {
                $groups[$n][] = $r['path'];
            }
        }
        $features = [];
        $seen = [];
        foreach ($groups as $n => $paths) {
            $n = (string) $n;
            $seen[$n] = true;
            $features[] = ['name' => $n, 'source' => 'route', 'paths' => $paths];
        }
        foreach ($this->flags as $f) {
            if (!isset($seen[$f['name']])) {
                $seen[$f['name']] = true;
                $features[] = $f;
            }
        }
        foreach ($env as $e) {
            foreach (['FEATURE_', 'FF_', 'ENABLE_', 'ENABLED_'] as $pfx) {
                if (str_starts_with($e, $pfx)) {
                    $n = strtolower(substr($e, strlen($pfx)));
                    if ($n !== '' && !isset($seen[$n])) {
                        $seen[$n] = true;
                        $features[] = ['name' => $n, 'source' => 'config', 'paths' => [$e]];
                    }
                }
            }
        }
        $dirs = array_keys($this->dirFiles);
        sort($dirs, SORT_STRING);
        $skipNames = ['src', 'lib', 'test', 'tests', 'spec', 'docs', 'scripts', 'public', 'static', 'assets', 'utils', 'common', 'types', 'config', 'migrations', 'database', 'bin', 'tmp', 'examples', 'e2e', '__tests__', 'node_modules', 'app'];
        foreach ($dirs as $d) {
            $n = strtolower(basename((string) $d));
            if (isset($seen[$n]) || str_starts_with($n, '.') || in_array($n, $skipNames, true)) {
                continue;
            }
            $seen[$n] = true;
            $features[] = ['name' => $n, 'source' => 'dir', 'paths' => [(string) $d]];
        }
        $features = array_slice($features, 0, self::MAX_FEATURES);
        $infra = $this->infra;
        $infra['ci'] = $this->sortedKeys($this->ci);
        $infra['hosting_hints'] = $this->sortedKeys($this->hosting);
        $langs = $this->langs;
        ksort($langs, SORT_STRING);

        return [
            'schema' => 1,
            'scanned_at' => gmdate('Y-m-d\TH:i:s\Z'),
            'root_name' => basename($abs),
            'languages' => $langs ?: new \stdClass(),
            'loc' => $this->loc,
            'frameworks' => $this->sortedKeys($fw),
            'dependencies' => $this->depList,
            'routes' => $this->routes,
            'features' => $features,
            'integrations' => $this->sortedKeys($integ),
            'infra' => $infra,
            'env_names' => $env,
            'pricing_hints' => $this->pricing,
            'description' => $this->description,
            'tests' => ['files' => $this->testFiles, 'frameworks' => $this->sortedKeys($testFw)],
            'security' => $this->security,
        ];
    }

    private function routeGroup(string $p): string
    {
        foreach (explode('/', $p) as $seg) {
            $l = strtolower($seg);
            if ($l === '' || $l === 'api' || $l === '*' || $l[0] === ':' || $l[0] === '{') {
                continue;
            }
            if (preg_match('~^v\d~', $l)) {
                continue;
            }
            return $l;
        }
        return '';
    }

    /** @param array<string,bool> $m */
    private function sortedKeys(array $m): array
    {
        $k = array_map('strval', array_keys(array_filter($m)));
        sort($k, SORT_STRING);
        return $k;
    }
}
