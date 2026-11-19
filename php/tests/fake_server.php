<?php
// Capture server for tests/run.php: php -S 127.0.0.1:<port> tests/fake_server.php  (env PROPHECY_CAPTURE = directory)
$dir = getenv('PROPHECY_CAPTURE') ?: sys_get_temp_dir();
$path = parse_url($_SERVER['REQUEST_URI'], PHP_URL_PATH);
$raw = file_get_contents('php://input');
$gz = (($_SERVER['HTTP_CONTENT_ENCODING'] ?? '') === 'gzip');
if ($gz && $raw !== '') {
    $raw = (string) @gzdecode($raw);
}
$failFile = $dir . '/fail';
if (is_file($failFile) && ($n = (int) file_get_contents($failFile)) > 0) {
    file_put_contents($failFile, (string) ($n - 1));
    http_response_code(503);
    echo '{"error":"busy"}';
    return true;
}
$rec = [
    'method' => $_SERVER['REQUEST_METHOD'],
    'uri' => $_SERVER['REQUEST_URI'],
    'path' => $path,
    'gzip' => $gz,
    'x_lj_key' => $_SERVER['HTTP_X_LJ_KEY'] ?? null,
    'authorization' => $_SERVER['HTTP_AUTHORIZATION'] ?? null,
    'user_agent' => $_SERVER['HTTP_USER_AGENT'] ?? null,
    'body' => json_decode($raw, true),
];
file_put_contents($dir . '/requests.jsonl', json_encode($rec) . "\n", FILE_APPEND | LOCK_EX);
header('Content-Type: application/json');
switch ($path) {
    case '/api/v1/prophecy/sdk/embed':
        http_response_code(200);
        echo '{"url":"https://linearjeph.cc/prophecy-embed/tok","expires_at":"2030-01-01T00:00:00Z"}';
        break;
    case '/api/v1/prophecy/sdk/import':
        http_response_code(200);
        $rows = $rec['body']['rows'] ?? [];
        echo json_encode(['job' => 1, 'accepted' => count($rows), 'rejected' => 0, 'errors' => []]);
        break;
    case '/api/v1/prophecy/sdk/report':
        http_response_code(200);
        echo '{"report":{"headline":{"score":71}},"created_at":"2030-01-01T00:00:00Z","stale":false}';
        break;
    case '/api/v1/prophecy/sdk/refresh':
        http_response_code(202);
        echo '{"queued":true}';
        break;
    case '/api/v1/prophecy/sdk/live':
        http_response_code(200);
        echo '{"active_5m":3}';
        break;
    default:
        http_response_code(202);
        echo '{"accepted":1,"rejected":0}';
}
return true;
