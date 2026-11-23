<?php
declare(strict_types=1);

// Self-contained test runner: php packages/sdk-php/tests/run.php   (no composer needed)
require __DIR__ . '/../src/autoload.php';

use Linearjeph\Prophecy;
use Linearjeph\Prophecy\Client;
use Linearjeph\Prophecy\Cli;
use Linearjeph\Prophecy\Middleware;
use Linearjeph\Prophecy\ProphecyException;
use Linearjeph\Prophecy\Routes;
use Linearjeph\Prophecy\Scanner;

error_reporting(E_ALL);
ini_set('error_log', sys_get_temp_dir() . '/prophecy-test-errors.log');

$passed = 0;
$failed = 0;
function check(string $name, bool $ok, string $detail = ''): void
{
    global $passed, $failed;
    if ($ok) {
        $passed++;
        echo "  ok   $name\n";
    } else {
        $failed++;
        echo "  FAIL $name" . ($detail !== '' ? " -- $detail" : '') . "\n";
    }
}
function eq(string $name, mixed $got, mixed $want): void
{
    check($name, $got === $want || (is_float($want) && is_numeric($got) && abs($got - $want) < 1e-9),
        'got ' . json_encode($got) . ' want ' . json_encode($want));
}
function section(string $s): void
{
    echo "$s\n";
}

// ---- capture server ----------------------------------------------------------------------------------------
$capDir = sys_get_temp_dir() . '/prophecy-cap-' . bin2hex(random_bytes(4));
mkdir($capDir);
$sock = stream_socket_server('tcp://127.0.0.1:0', $en, $es);
$port = (int) substr(strrchr(stream_socket_get_name($sock, false), ':'), 1);
fclose($sock);
$proc = proc_open([PHP_BINARY, '-S', "127.0.0.1:$port", __DIR__ . '/fake_server.php'], [0 => ['pipe', 'r'], 1 => ['file', '/dev/null', 'w'], 2 => ['file', '/dev/null', 'w']], $pipes, __DIR__, ['PROPHECY_CAPTURE' => $capDir, 'PATH' => getenv('PATH')]);
register_shutdown_function(static function () use ($proc, $capDir): void {
    proc_terminate($proc);
    foreach (glob($capDir . '/*') ?: [] as $f) {
        @unlink($f);
    }
    @rmdir($capDir);
});
for ($i = 0; $i < 100; $i++) {
    $c = @fsockopen('127.0.0.1', $port, $e1, $e2, 0.1);
    if ($c) {
        fclose($c);
        break;
    }
    usleep(50000);
}
$endpoint = "http://127.0.0.1:$port";

function captured(): array
{
    global $capDir;
    $f = $capDir . '/requests.jsonl';
    if (!is_file($f)) {
        return [];
    }
    return array_map(static fn ($l) => json_decode($l, true), array_filter(explode("\n", (string) file_get_contents($f))));
}
function reset_capture(): void
{
    global $capDir;
    @unlink($capDir . '/requests.jsonl');
    @unlink($capDir . '/fail');
}
function client(array $o = []): Client
{
    global $endpoint;
    return new Client($o + ['ingestKey' => 'lj_app_x', 'secretKey' => 'ljp_sk_secret', 'endpoint' => $endpoint, 'backoffMs' => 1, 'registerShutdown' => false]);
}
function events(): array
{
    $out = [];
    foreach (captured() as $r) {
        if ($r['path'] === '/ingest/v1/prophecy/events') {
            foreach ($r['body']['events'] as $e) {
                $out[] = $e;
            }
        }
    }
    return $out;
}

// ---- tests -------------------------------------------------------------------------------------------------
section('batching, gzip, hashing, auth');
reset_capture();
$c = client(['flushAt' => 0]);
for ($i = 0; $i < 250; $i++) {
    $c->track('thing', ['uid' => 'user-1', 'props' => ['i' => $i]]);
}
check('buffered until flush (flushAt 0)', captured() === []);
check('flush empties buffer', $c->flush() === true);
$reqs = array_values(array_filter(captured(), static fn ($r) => $r['path'] === '/ingest/v1/prophecy/events'));
eq('3 requests for 250 events (batch 100)', count($reqs), 3);
eq('batch sizes', array_map(static fn ($r) => count($r['body']['events']), $reqs), [100, 100, 50]);
$hasGz = function_exists('gzencode');
eq('gzip when zlib present', $reqs[0]['gzip'], $hasGz);
eq('ingest key header', $reqs[0]['x_lj_key'], 'lj_app_x');
eq('no bearer on ingest', $reqs[0]['authorization'], null);
$e = $reqs[0]['body']['events'][0];
eq('uid hashed sha256', $e['uid'], hash('sha256', 'user-1'));
eq('default kind', $e['kind'], 'custom');
check('ts iso8601 ms', (bool) preg_match('~^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$~', $e['ts']), $e['ts']);
check('user agent', str_starts_with((string) $reqs[0]['user_agent'], 'linearjeph-prophecy-php/1.3.0'));

section('auto flush at batch size');
reset_capture();
$c = client();
for ($i = 0; $i < 100; $i++) {
    $c->track('x');
}
eq('sent without explicit flush at 100', count(events()), 100);

section('hash disabled + helpers');
reset_capture();
$c = client(['hashUserIds' => false, 'flushAt' => 0]);
$c->feature('export', 'raw-id');
$c->signup('u1', ['plan' => 'free', 'channel' => 'ads', 'country' => 'NG']);
$c->payment('u1', 49.5, 'usd', ['plan' => 'pro']);
$c->payment('u1', -10);
$c->subscription('u1', ['plan' => 'pro', 'mrr' => 49.5, 'status' => 'active']);
$c->churn('u1', 'price');
$c->identify('u1', ['company' => 'Acme', 'plan' => 'pro']);
$c->flush();
$byKind = [];
foreach (events() as $ev) {
    $byKind[$ev['kind']] = $ev;
}
eq('feature uid raw', $byKind['feature']['uid'], 'raw-id');
eq('feature field', $byKind['feature']['feature'], 'export');
eq('payment amount', $byKind['payment']['amount'], 49.5);
eq('payment currency upper', $byKind['payment']['currency'], 'USD');
eq('refund amount', $byKind['refund']['amount'], -10);
eq('refund default currency', $byKind['refund']['currency'], 'USD');
eq('subscription props', $byKind['subscription']['props'], ['plan' => 'pro', 'mrr' => 49.5, 'status' => 'active']);
eq('churn reason', $byKind['churn']['props']['reason'], 'price');
eq('signup country', $byKind['signup']['props']['country'], 'NG');
$custs = [];
foreach (captured() as $r) {
    if ($r['path'] === '/ingest/v1/prophecy/identify') {
        array_push($custs, ...$r['body']['customers']);
    }
}
eq('3 customer updates', count($custs), 3);
eq('identify plan field', $custs[2]['plan'], 'pro');
eq('identify traits', $custs[2]['traits'], ['company' => 'Acme']);
check('signup identifies signup_at', isset($custs[0]['signup_at']));

section('retries on 5xx');
reset_capture();
file_put_contents($capDir . '/fail', '2');
$c = client(['flushAt' => 0]);
$c->track('x');
$c->flush();
eq('delivered after 2x 503', count(events()), 1);

section('never throws');
$bad = new Client(['ingestKey' => 'k', 'endpoint' => 'http://127.0.0.1:1', 'retries' => 0, 'registerShutdown' => false, 'timeout' => 1]);
$threw = false;
try {
    $bad->track('');
    $bad->track('x', ['props' => ['bad' => NAN, 'obj' => fopen('php://memory', 'r')]]);
    $bad->track('x', ['amount' => 'abc', 'ts' => 'not a date at all']);
    $bad->identify('');
    $bad->signup('');
    $bad->payment('u', 1);
    $bad->recordRequest('', '', 0, 0.0);
    $bad->flush();
    $bad->close();
    $bad->track('after-close');
    Prophecy::setClient(null);
    Prophecy::track('x');
    Prophecy::feature('x');
    Prophecy::flush();
} catch (Throwable $t) {
    $threw = true;
    echo '    ' . $t->getMessage() . "\n";
}
check('no exception from tracking calls', !$threw);
$caught = false;
try {
    Prophecy::report();
} catch (ProphecyException) {
    $caught = true;
}
check('report before init throws ProphecyException', $caught);
$caught = false;
try {
    new Client([]);
} catch (InvalidArgumentException) {
    $caught = true;
}
check('missing key rejected', $caught);

section('queue bounded');
$c = client(['queueSize' => 10, 'flushAt' => 0]);
for ($i = 0; $i < 25; $i++) {
    $c->track('x');
}
eq('dropped', $c->dropped(), 15);

section('secret key API');
reset_capture();
$c = client();
$r = $c->report('revenue');
eq('report body', $r['report']['headline']['score'], 71);
eq('refresh queued', $c->refresh()['queued'], true);
eq('live', $c->live()['active_5m'], 3);
eq('embed url', $c->embedUrl(600), 'https://linearjeph.cc/prophecy-embed/tok');
$rq = captured();
eq('paths', array_column($rq, 'uri'), ['/api/v1/prophecy/sdk/report?section=revenue', '/api/v1/prophecy/sdk/refresh', '/api/v1/prophecy/sdk/live', '/api/v1/prophecy/sdk/embed']);
eq('bearer', $rq[0]['authorization'], 'Bearer ljp_sk_secret');
eq('no ingest header on secret calls', $rq[0]['x_lj_key'], null);
eq('ttl', $rq[3]['body']['ttl'], 600);
$caught = false;
try {
    client(['secretKey' => ''])->report();
} catch (ProphecyException) {
    $caught = true;
}
check('secret call without secret key throws', $caught);

section('import batching');
reset_capture();
$c = client();
$rows = [];
for ($i = 0; $i < 12000; $i++) {
    $rows[] = ['uid' => 'u', 'name' => 'x', 'ts' => '2024-01-01T00:00:00Z', 'id' => $i];
}
$prog = [];
$res = $c->importHistory('events', $rows, function (int $d, int $t) use (&$prog): void {
    $prog[] = [$d, $t];
});
$rq = captured();
eq('3 import requests', count($rq), 3);
eq('batch sizes', array_map(static fn ($r) => count($r['body']['rows']), $rq), [5000, 5000, 2000]);
eq('uid hashed in import', $rq[0]['body']['rows'][0]['uid'], hash('sha256', 'u'));
eq('kind sent', $rq[0]['body']['kind'], 'events');
eq('progress', $prog, [[5000, 12000], [10000, 12000], [12000, 12000]]);
eq('accepted merged', $res['accepted'], 12000);
$caught = false;
try {
    $c->importHistory('nope', []);
} catch (ProphecyException) {
    $caught = true;
}
check('bad kind throws', $caught);

section('manifest');
reset_capture();
client()->sendManifest(['schema' => 1, 'root_name' => 'x']);
$rq = captured();
eq('manifest path', $rq[0]['path'], '/ingest/v1/prophecy/manifest');
eq('manifest body', $rq[0]['body']['manifest']['root_name'], 'x');
eq('manifest uses ingest key', $rq[0]['x_lj_key'], 'lj_app_x');

section('route normalisation + middleware');
eq('numeric', Routes::normalize('/users/42/orders/7?x=1'), '/users/:id/orders/:id');
eq('uuid', Routes::normalize('/f/3f2b8c1e-1234-4abc-9def-0123456789ab'), '/f/:id');
eq('hex', Routes::normalize('/t/0123456789abcdef0123'), '/t/:id');
eq('plain', Routes::normalize('/about'), '/about');
eq('empty', Routes::normalize(''), '/');
eq('pattern', Routes::fromPattern('users/{id}/orders/{order?}'), '/users/:id/orders/:order');
reset_capture();
Prophecy::setClient(client(['flushAt' => 0]));
$mw = Prophecy::middleware();
$req = new class {
    public function getMethod() { return 'get'; }
    public function getUri() { return new class { public function getPath() { return '/users/123'; } }; }
    public function getAttribute($n) { return null; }
};
$ok = new class { public function handle($r) { return new class { public function getStatusCode() { return 201; } }; } };
$boom = new class { public function handle($r) { throw new RuntimeException('boom'); } };
$mw->process($req, $ok);
$rethrown = false;
try {
    $mw->process($req, $boom);
} catch (RuntimeException) {
    $rethrown = true;
}
check('middleware rethrows handler errors', $rethrown);
$slimReq = new class {
    public function getMethod() { return 'POST'; }
    public function getUri() { return new class { public function getPath() { return '/orders/5'; } }; }
    public function getAttribute($n) { return new class { public function getPattern() { return '/orders/{id}'; } }; }
};
$mw($slimReq, $ok);
Prophecy::flush();
$ev = events();
eq('3 request events', count($ev), 3);
eq('route template from path', $ev[0]['props']['route'], '/users/:id');
eq('status', $ev[0]['props']['status'], 201);
eq('method upper', $ev[0]['props']['method'], 'GET');
check('duration', is_numeric($ev[0]['props']['duration_ms']));
eq('kind request', $ev[0]['kind'], 'request');
check('no uid on requests', !isset($ev[0]['uid']));
eq('error status 500', $ev[1]['props']['status'], 500);
eq('slim route pattern', $ev[2]['props']['route'], '/orders/:id');
// laravel
reset_capture();
$lreq = new class {
    public function route() { return new class { public function uri() { return 'api/users/{user}'; } }; }
    public function method() { return 'DELETE'; }
    public function path() { return 'api/users/9'; }
};
(new Linearjeph\Prophecy\Laravel\TrackRequests())->handle($lreq, fn ($r) => new class { public function getStatusCode() { return 204; } });
Prophecy::flush();
$ev = events();
eq('laravel route', $ev[0]['props']['route'], '/api/users/:user');
eq('laravel status', $ev[0]['props']['status'], 204);
Prophecy::recordRequest('/x/:id', 'post', 404, 1.54);
Prophecy::flush();
$ev = events();
eq('recordRequest', [$ev[1]['props']['method'], $ev[1]['props']['status'], $ev[1]['props']['duration_ms']], ['POST', 404, 1.5]);
Prophecy::setClient(null);

section('scanner fixture');
function put(string $root, string $rel, string $body): void
{
    $p = $root . '/' . $rel;
    @mkdir(dirname($p), 0777, true);
    file_put_contents($p, $body);
}
$base = sys_get_temp_dir() . '/prophecy-scan-' . bin2hex(random_bytes(4));
$root = $base . '/shop';
put($root, 'go.mod', "module x\n\ngo 1.21\n\nrequire (\n\tgithub.com/go-chi/chi/v5 v5.0.10\n\tgithub.com/stripe/stripe-go/v76 v76.1.0\n\tgolang.org/x/sys v0.1.0 // indirect\n)\n");
put($root, 'go.sum', 'x');
put($root, 'main.go', "package main\nfunc main() {\n\tmux.HandleFunc(\"GET /health\", h)\n\tr.Get(\"/api/v1/orders/{id}\", h)\n\tapi := r.Group(\"/api\")\n\tapi.Post(\"/invoices\", h)\n\tg.GET(\"/ping\", h)\n\tx := os.Getenv(\"STRIPE_SECRET_KEY\")\n}\n");
put($root, 'routes/web.php', "<?php\nRoute::get('/posts/{post}', f);\nRoute::resource('photos', P::class);\nif (Feature::active('beta-dash')) {}\n\$k = env('FEATURE_DARK_MODE');\n");
put($root, 'config/routes.rb', "Rails.application.routes.draw do\n  namespace :admin do\n    resources :users\n    get '/stats', to: 'x#y'\n  end\n  post '/login', to: 's#c'\nend\n");
put($root, 'web/server.js', "app.get('/express/items', h)\nconst k = process.env.SENTRY_DSN\n");
put($root, 'app/dashboard/page.tsx', "export default 1\n");
put($root, 'pyapp/urls.py', "urlpatterns = [path('articles/<int:pk>/', v), path('', v)]\n");
put($root, 'package.json', '{"dependencies":{"express":"^4.18.0","react":"18.2.0"},"devDependencies":{"jest":"29.0.0"}}');
put($root, 'composer.json', '{"require":{"php":">=8.1","laravel/framework":"^10.0","ext-json":"*"}}');
put($root, 'Gemfile.lock', "GEM\n  remote: https://rubygems.org/\n  specs:\n    rails (7.1.0)\n      actioncable (= 7.1.0)\n    rspec (3.12.0)\n\nPLATFORMS\n  ruby\n");
put($root, 'requirements.txt', "# c\nDjango==4.2.1\nrequests>=2.0\n-r other.txt\n");
put($root, 'pyproject.toml', "[project]\ndependencies = [\n  \"fastapi>=0.100\",\n  \"pydantic\",\n]\n");
put($root, 'README.md', "# Shop\n\nA [store](http://x) for **things**.\n" . str_repeat('filler ', 600));
put($root, '.env', "DATABASE_URL=postgres://user:supersecretvalue@db/x\nPAYSTACK_KEY=sk_test_abc\n");
put($root, '.env.example', "REDIS_URL=\nexport MIXPANEL_TOKEN=\n");
put($root, 'Dockerfile', "FROM scratch\n");
put($root, 'docker-compose.yml', "services: {}\n");
put($root, '.github/workflows/ci.yml', "on: push\n");
put($root, 'vercel.json', '{}');
put($root, 'tests/FooTest.php', "<?php\nclass FooTest {}\n");
put($root, 'billing/plans.json', '{"plans":[{"name":"starter","price":29,"currency":"USD"},{"name":"pro","price":99}]}');
put($root, 'billing/keys.go', "package billing\nvar k = \"AKIA" . "ABCDEFGHIJKLMNOP\"\nvar s = \"sk_" . "live_abcdefghijklmnopqrstuv\"\n");
put($root, 'node_modules/leak/index.js', "app.get('/leak', h)\n");
put($root, 'vendor/x/y.php', "<?php Route::get('/vendorleak', f);\n");
put($root, '.git/config', 'x');
put($root, 'big.js', '// ' . str_repeat('a', 1100 * 1024));
put($base . '/outside', 'evil.js', "app.get('/escaped', h)\n");
@symlink($base . '/outside', $root . '/link');
@symlink($base . '/outside/evil.js', $root . '/evil-link.js');

$m = Prophecy::scan($root);
eq('schema', $m['schema'], 1);
eq('root_name', $m['root_name'], 'shop');
eq('go files', $m['languages']['go'] ?? 0, 2);
eq('js files (big.js and symlink excluded)', $m['languages']['js'] ?? 0, 1);
check('loc', $m['loc'] > 20);
$routes = [];
foreach ($m['routes'] as $r) {
    $routes[$r['method'] . ' ' . $r['path']] = true;
}
foreach (['GET /health', 'GET /api/v1/orders/:id', 'POST /api/invoices', 'GET /ping', 'GET /posts/:post', 'GET /photos', 'DELETE /photos/:id', 'GET /admin/users', 'GET /admin/stats', 'POST /login', 'GET /express/items', 'GET /dashboard', 'ANY /articles/:pk/', 'ANY /'] as $w) {
    check("route $w", isset($routes[$w]));
}
check('no leaked routes', !isset($routes['GET /leak']) && !isset($routes['GET /escaped']) && !isset($routes['GET /vendorleak']));
$deps = [];
foreach ($m['dependencies'] as $d) {
    $deps[$d['ecosystem'] . ':' . $d['name']] = $d['version'];
}
foreach (['go:github.com/go-chi/chi/v5' => 'v5.0.10', 'npm:express' => '^4.18.0', 'npm:jest' => '29.0.0', 'composer:laravel/framework' => '^10.0', 'gem:rails' => '7.1.0', 'pypi:django' => '4.2.1', 'pypi:requests' => '>=2.0', 'pypi:fastapi' => '>=0.100', 'pypi:pydantic' => ''] as $k => $v) {
    check("dep $k", ($deps[$k] ?? null) === $v, json_encode($deps[$k] ?? null));
}
check('no indirect/php deps', !isset($deps['go:golang.org/x/sys']) && !isset($deps['composer:php']) && !isset($deps['composer:ext-json']));
foreach (['chi', 'express', 'react', 'laravel', 'rails', 'fastapi', 'django'] as $f) {
    check("framework $f", in_array($f, $m['frameworks'], true));
}
foreach (['stripe', 'paystack', 'sentry', 'mixpanel', 'redis'] as $i) {
    check("integration $i", in_array($i, $m['integrations'], true));
}
foreach (['STRIPE_SECRET_KEY', 'DATABASE_URL', 'REDIS_URL', 'MIXPANEL_TOKEN', 'SENTRY_DSN', 'FEATURE_DARK_MODE'] as $n) {
    check("env $n", in_array($n, $m['env_names'], true));
}
$feat = [];
foreach ($m['features'] as $f) {
    $feat[$f['name']] = $f['source'];
}
eq('feature route', $feat['orders'] ?? null, 'route');
eq('feature flag', $feat['beta-dash'] ?? null, 'flag');
eq('feature config', $feat['dark_mode'] ?? null, 'config');
eq('feature dir', $feat['billing'] ?? null, 'dir');
check('infra', $m['infra']['docker'] && $m['infra']['compose'] && in_array('github-actions', $m['infra']['ci'], true) && in_array('vercel', $m['infra']['hosting_hints'], true));
eq('test files', $m['tests']['files'], 1);
check('test frameworks', in_array('phpunit', $m['tests']['frameworks'], true) && in_array('jest', $m['tests']['frameworks'], true));
check('security', $m['security']['lockfile'] && $m['security']['env_committed'] && $m['security']['secrets_suspected'] >= 2, json_encode($m['security']));
check('description trimmed and stripped', strlen($m['description']) <= 2048 && str_starts_with($m['description'], 'Shop A store for things'));
$prices = [];
foreach ($m['pricing_hints'] as $p) {
    $prices[$p['plan']] = $p['amount'];
}
eq('pricing starter', $prices['starter'] ?? null, 29.0);
eq('pricing pro', $prices['pro'] ?? null, 99.0);
$json = json_encode($m);
foreach (['supersecretvalue', 'postgres://', 'AKIA' . 'ABCDEFGHIJKLMNOP', 'sk_live_abc', 'sk_test_abc', 'package main', 'mux.HandleFunc'] as $bad) {
    check("manifest does not leak $bad", !str_contains($json, $bad));
}
@mkdir($base . '/emptydir');
check('empty languages encode as JSON object', str_contains(json_encode((new Scanner())->scan($base . '/emptydir')), '"languages":{}'));

section('cli');
$tmpCsv = $base . '/events.csv';
file_put_contents($tmpCsv, "ts,uid,name,kind,amount,plan,source\n2024-01-01T00:00:00Z,u1,payment,payment,10.5,pro,web\n2024-01-02T00:00:00Z,u2,login,login,,,\n");
$rows = Cli::loadRows($tmpCsv, 'events');
eq('csv rows', count($rows), 2);
eq('csv amount numeric', $rows[0]['amount'], 10.5);
eq('csv extra column -> props', $rows[0]['props'], ['source' => 'web']);
$o = fopen('php://memory', 'w+');
$er = fopen('php://memory', 'w+');
eq('scan exit code', Cli::run(['scan', $base . '/outside'], $o, $er), 0);
rewind($o);
$printed = json_decode((string) stream_get_contents($o), true);
eq('scan prints manifest json', $printed['schema'] ?? null, 1);
eq('usage exit code', Cli::run([], $o, $er), 2);

// cleanup scan fixture
$rm = static function (string $d) use (&$rm): void {
    foreach (scandir($d) ?: [] as $n) {
        if ($n === '.' || $n === '..') {
            continue;
        }
        $p = "$d/$n";
        is_link($p) || !is_dir($p) ? @unlink($p) : $rm($p);
    }
    @rmdir($d);
};
$rm($base);

require __DIR__ . '/products_test.php';

echo "\n$passed passed, $failed failed\n";
exit($failed === 0 ? 0 : 1);
