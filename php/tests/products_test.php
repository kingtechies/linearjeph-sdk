<?php
declare(strict_types=1);

// Product API client tests. Included by run.php (uses its check/eq/section helpers); `php tests/products_test.php` also works.
if (!function_exists('check')) {
    require __DIR__ . '/../src/autoload.php';
    $passed = 0;
    $failed = 0;
    function check(string $name, bool $ok, string $detail = ''): void
    {
        global $passed, $failed;
        $ok ? $passed++ : $failed++;
        echo ($ok ? '  ok   ' : '  FAIL ') . $name . ($ok || $detail === '' ? '' : " -- $detail") . "\n";
    }
    function eq(string $name, mixed $got, mixed $want): void
    {
        check($name, $got === $want, 'got ' . json_encode($got) . ' want ' . json_encode($want));
    }
    function section(string $s): void
    {
        echo "$s\n";
    }
    $standalone = true;
}

use LinearJeph\Products\ApiError;
use LinearJeph\Products\Client as LJ;

section('products client');

$pdir = sys_get_temp_dir() . '/products-cap-' . bin2hex(random_bytes(4));
mkdir($pdir);
$s = stream_socket_server('tcp://127.0.0.1:0', $en, $es);
$pport = (int) substr(strrchr(stream_socket_get_name($s, false), ':'), 1);
fclose($s);
$pproc = proc_open([PHP_BINARY, '-S', "127.0.0.1:$pport", __DIR__ . '/products_server.php'], [0 => ['pipe', 'r'], 1 => ['file', '/dev/null', 'w'], 2 => ['file', '/dev/null', 'w']], $pp, __DIR__, ['PRODUCTS_CAPTURE' => $pdir, 'PATH' => getenv('PATH')]);
register_shutdown_function(static function () use ($pproc, $pdir): void {
    proc_terminate($pproc);
    foreach (glob($pdir . '/*') ?: [] as $f) {
        @unlink($f);
    }
    @rmdir($pdir);
});
for ($i = 0; $i < 100; $i++) {
    $c = @fsockopen('127.0.0.1', $pport, $e1, $e2, 0.1);
    if ($c) {
        fclose($c);
        break;
    }
    usleep(50000);
}

$fakeKey = 'ljk_' . 'test1234_' . str_repeat('x', 32);
$base = "http://127.0.0.1:$pport";
$script = static function (array $s) use ($pdir): void {
    foreach (glob($pdir . '/count-*') ?: [] as $f) {
        unlink($f);
    }
    file_put_contents($pdir . '/script.json', json_encode($s));
};
$reqs = static function () use ($pdir): array {
    $f = $pdir . '/requests.jsonl';
    return is_file($f) ? array_map(static fn($l) => json_decode($l, true), array_filter(explode("\n", (string) file_get_contents($f)))) : [];
};
$reset = static function () use ($pdir, $script): void {
    @unlink($pdir . '/requests.jsonl');
    $script([]);
};
$mk = static fn(array $o = []) => new LJ(apiKey: $fakeKey, baseUrl: $base, retries: $o['retries'] ?? 2, backoffMs: 1, pollInterval: 0.01, org: $o['org'] ?? null);

// construction
putenv('LJ_API_KEY');
$threw = false;
try {
    new LJ();
} catch (\InvalidArgumentException) {
    $threw = true;
}
check('missing key throws', $threw);
putenv("LJ_API_KEY=$fakeKey");
putenv('LJ_BASE_URL=http://example.invalid/');
$ref = new LJ();
eq('env key accepted', $ref instanceof LJ, true);
putenv('LJ_API_KEY');
putenv('LJ_BASE_URL');
eq('version', LJ::VERSION, '1.3.0');
check('Linearjeph prefix also autoloads', class_exists('Linearjeph\Products\ApiError'));

// headers + whoami
$reset();
$script(['GET /api/v1/apikeys/whoami' => [[200, ['organization' => ['id' => 1, 'name' => 'Acme'], 'scopes' => ['fix']]]]]);
$who = $mk()->whoami();
$r = $reqs()[0];
eq('whoami name', $who['organization']['name'], 'Acme');
eq('bearer header', $r['authorization'], "Bearer $fakeKey");
eq('user agent', $r['user_agent'], 'linearjeph-php/1.3.0');

// errors
$reset();
$script(['GET /api/v1/fixops/jobs/9' => [[404, ['error' => ['code' => 'not_found', 'message' => 'Job not found.']]]]]);
try {
    $mk()->fix->job(9);
    check('404 throws', false);
} catch (ApiError $e) {
    eq('error status', $e->status(), 404);
    eq('error code', $e->errorCode(), 'not_found');
    eq('error message', $e->getMessage(), 'Job not found.');
}
eq('404 not retried', count($reqs()), 1);

// GET retry on 503 then 200, honouring Retry-After
$reset();
$script(['GET /api/v1/fixops/overview' => [[503, ['error' => ['code' => 'busy', 'message' => 'x']], ['Retry-After' => '0']], [429, '{}'], [200, ['ok' => true]]]]);
$o = $mk()->fix->overview();
eq('retry succeeds', $o['ok'] ?? null, true);
eq('three attempts', count($reqs()), 3);

// GET gives up after three tries
$reset();
$script(['GET /api/v1/fixops/overview' => [[500, ['error' => ['code' => 'boom', 'message' => 'x']]]]]);
try {
    $mk()->fix->overview();
    check('gives up', false);
} catch (ApiError $e) {
    eq('final status', $e->status(), 500);
}
eq('exactly three tries', count($reqs()), 3);

// POST not retried
$reset();
$script(['POST /api/v1/fixops/jobs/3/cancel' => [[500, ['error' => ['code' => 'boom', 'message' => 'x']]]]]);
try {
    $mk()->fix->cancelJob(3);
} catch (ApiError) {
}
eq('post sent once', count($reqs()), 1);

// network error
$dead = new LJ(apiKey: $fakeKey, baseUrl: 'http://127.0.0.1:1', retries: 0, timeout: 1.0);
try {
    $dead->fix->overview();
    check('network error throws', false);
} catch (ApiError $e) {
    eq('network error status 0', $e->status(), 0);
}

// route map
$reset();
$lj = $mk(['org' => '7']);
$cases = [
    ['GET', '/api/v1/fixops/overview', fn() => $lj->fix->overview()],
    ['POST', '/api/v1/fixops/jobs', fn() => $lj->fix->createJob(['source_id' => 's', 'issue' => 'bug'])],
    ['GET', '/api/v1/fixops/jobs/3/events', fn() => $lj->fix->jobEvents(3, 9)],
    ['POST', '/api/v1/fixops/jobs/3/reject', fn() => $lj->fix->rejectJob(3, 'no')],
    ['POST', '/api/v1/fixops/jobs/3/rollback', fn() => $lj->fix->rollbackJob(3)],
    ['PATCH', '/api/v1/fixops/projects/2', fn() => $lj->fix->updateProject(2, ['deps_enabled' => true])],
    ['POST', '/api/v1/fixops/projects/2/review-now', fn() => $lj->fix->review(2)],
    ['POST', '/api/v1/fixops/projects/2/deps-now', fn() => $lj->fix->deps(2)],
    ['POST', '/api/v1/fixops/projects/2/tests-now', fn() => $lj->fix->tests(2, 4)],
    ['POST', '/api/v1/fixops/migrations/5/plan', fn() => $lj->fix->migrationPlan(5)],
    ['POST', '/api/v1/shield/vibe/projects/4/scan', fn() => $lj->shield->vibeScan(4)],
    ['GET', '/api/v1/shield/vibe/scans/8', fn() => $lj->shield->vibeScanDetail(8)],
    ['GET', '/api/v1/shield/secrets/findings', fn() => $lj->shield->secretFindings('open')],
    ['POST', '/api/v1/shield/pentest/targets/6/scan', fn() => $lj->shield->pentestScan(6)],
    ['POST', '/api/v1/shield/pentest/findings/6/retest', fn() => $lj->shield->pentestRetest(6)],
    ['GET', '/api/v1/shield/waf/domains/6/events', fn() => $lj->shield->wafEvents(6)],
    ['POST', '/api/v1/security/scans', fn() => $lj->shield->securityScan(6)],
    ['POST', '/api/v1/opsagent/incidents/2/resolve', fn() => $lj->ops->resolveIncident(2)],
    ['POST', '/api/v1/opsagent/cost/analyse', fn() => $lj->ops->costAnalyse()],
    ['GET', '/api/v1/opsagent/managed-db', fn() => $lj->ops->managedDatabases()],
    ['GET', '/api/v1/backups/agent/targets', fn() => $lj->ops->backupTargets()],
    ['POST', '/api/v1/backups/agent/targets/3/drill', fn() => $lj->ops->runDrill(3)],
    ['GET', '/api/v1/backups/agent/targets/3/runs', fn() => $lj->ops->backupRuns(3)],
    ['GET', '/api/v1/endpoints/endpoints', fn() => $lj->models->endpoints()],
    ['POST', '/api/v1/endpoints/endpoints/1/suspend', fn() => $lj->models->endpointAction(1, 'suspend')],
    ['GET', '/api/v1/agentcloud/private/endpoints/1/logs', fn() => $lj->models->privateLogs(1)],
    ['GET', '/api/v1/agentcloud/finetune/jobs', fn() => $lj->models->finetuneJobs()],
    ['GET', '/api/v1/agentcloud/agents/1/runs/9', fn() => $lj->agents->run(1, 9)],
    ['POST', '/api/v1/agentcloud/agents/1/run', fn() => $lj->agents->action(1, 'run', ['q' => 1])],
    ['POST', '/api/v1/comply/compliance/recheck', fn() => $lj->comply->recheck()],
    ['GET', '/api/v1/comply/compliance/tasks', fn() => $lj->comply->tasks()],
];
foreach ($cases as [$m, $p, $fn]) {
    $fn();
}
$got = $reqs();
$allOk = true;
foreach ($cases as $i => [$m, $p]) {
    $okRow = ($got[$i]['method'] ?? '') === $m && ($got[$i]['path'] ?? '') === $p;
    $allOk = $allOk && $okRow;
    if (!$okRow) {
        check("route $m $p", false, 'got ' . ($got[$i]['method'] ?? '?') . ' ' . ($got[$i]['path'] ?? '?'));
    }
}
check('all ' . count($cases) . ' routes use the right method and path', $allOk);
eq('create job body', $got[1]['body'], ['source_id' => 's', 'issue' => 'bug']);
eq('json content type', $got[1]['content_type'], 'application/json');
check('org query sent', str_contains($got[2]['uri'], 'org=7') && str_contains($got[2]['uri'], 'after=9'), $got[2]['uri']);
eq('reject reason', $got[3]['body'], ['reason' => 'no']);
eq('tests max_files', $got[8]['body'], ['max_files' => 4]);
eq('agent run input', $got[28]['body'], ['input' => ['q' => 1]]);

// generic request, empty, non json, list
$reset();
$script([
    'GET /api/v1/market/catalog' => [[200, ['results' => [1]]]],
    'POST /api/v1/x/empty' => [[204, '']],
    'GET /api/v1/x/text' => [[200, "not json\n", ['Content-Type' => 'text/plain']]],
    'GET /api/v1/x/list' => [[200, '[1,2]']],
]);
eq('generic request', $mk()->request('get', '/api/v1/market/catalog', null, ['q' => 'a b'])['results'], [1]);
eq('empty body is []', $mk()->request('POST', '/api/v1/x/empty', []), []);
eq('text is raw', $mk()->request('GET', '/api/v1/x/text')['raw'], "not json\n");
eq('list wrapped in data', $mk()->request('GET', '/api/v1/x/list')['data'], [1, 2]);
check('query encoded', str_contains($reqs()[0]['uri'], 'q=a%20b') || str_contains($reqs()[0]['uri'], 'q=a+b'));

// downloads
$reset();
$script(['GET /api/v1/fixops/jobs/2/patch' => [[200, "--- a\n+++ b\n", ['Content-Type' => 'text/x-diff']]]]);
eq('patch text', $mk()->fix->jobPatch(2), "--- a\n+++ b\n");

// fix wait
$reset();
$script(['GET /api/v1/fixops/jobs/5' => [[200, ['id' => 5, 'status' => 'patching']], [200, ['id' => 5, 'status' => 'testing']], [200, ['id' => 5, 'status' => 'pr_open']]]]);
$job = $mk()->fix->wait(5, 5);
eq('wait returns final job', $job['status'], 'pr_open');
eq('wait polled three times', count($reqs()), 3);
$reset();
$script(['GET /api/v1/fixops/jobs/5' => [[200, ['id' => 5, 'status' => 'patching']]]]);
try {
    $mk()->fix->wait(5, 0.05);
    check('wait times out', false);
} catch (\RuntimeException $e) {
    check('wait times out', !($e instanceof ApiError));
}

// shield gate + wait
$lj = $mk();
$scan = ['findings' => [['severity' => 'low'], ['severity' => 'high'], ['severity' => 'critical'], ['severity' => 'info']]];
eq('gate default high', count($lj->shield->gate($scan)), 2);
eq('gate critical', count($lj->shield->gate($scan, 'critical')), 1);
eq('gate low', count($lj->shield->gate($scan, 'low')), 3);
eq('gate medium', count($lj->shield->gate($scan, 'Medium')), 2);
$threw = false;
try {
    $lj->shield->gate($scan, 'severe');
} catch (\InvalidArgumentException) {
    $threw = true;
}
check('gate rejects unknown severity', $threw);
$reset();
$script(['GET /api/v1/shield/vibe/scans/8' => [[200, ['id' => 8, 'status' => 'running']], [200, ['id' => 8, 'status' => 'done', 'findings' => []]]]]);
eq('waitVibeScan', $lj->shield->waitVibeScan(8, 5)['status'], 'done');

if (!empty($standalone)) {
    echo "\n$passed passed, $failed failed\n";
    exit($failed === 0 ? 0 : 1);
}
