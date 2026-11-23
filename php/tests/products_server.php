<?php
// Fake product API for tests/products_test.php: php -S 127.0.0.1:<port> tests/products_server.php (env PRODUCTS_CAPTURE = dir)
// Responses come from <dir>/script.json: {"METHOD /path": [[status, body, {headers}], ...]}; each entry is used once, the last repeats.
$dir = getenv('PRODUCTS_CAPTURE') ?: sys_get_temp_dir();
$path = parse_url($_SERVER['REQUEST_URI'], PHP_URL_PATH);
$key = $_SERVER['REQUEST_METHOD'] . ' ' . $path;
file_put_contents($dir . '/requests.jsonl', json_encode([
    'method' => $_SERVER['REQUEST_METHOD'],
    'uri' => $_SERVER['REQUEST_URI'],
    'path' => $path,
    'authorization' => $_SERVER['HTTP_AUTHORIZATION'] ?? null,
    'user_agent' => $_SERVER['HTTP_USER_AGENT'] ?? null,
    'content_type' => $_SERVER['CONTENT_TYPE'] ?? null,
    'body' => json_decode(file_get_contents('php://input'), true),
]) . "\n", FILE_APPEND | LOCK_EX);
$script = json_decode((string) @file_get_contents($dir . '/script.json'), true) ?: [];
$entries = $script[$key] ?? null;
if ($entries === null) {
    http_response_code(200);
    header('Content-Type: application/json');
    echo '{"results":[]}';
    return true;
}
$countFile = $dir . '/count-' . md5($key);
$n = (int) @file_get_contents($countFile);
file_put_contents($countFile, (string) ($n + 1));
[$status, $body, $headers] = $entries[min($n, count($entries) - 1)] + [2 => []];
http_response_code($status);
foreach ($headers as $k => $v) {
    header("$k: $v");
}
if (!isset($headers['Content-Type'])) {
    header('Content-Type: application/json');
}
echo is_string($body) ? $body : json_encode($body);
return true;
