<?php
declare(strict_types=1);

namespace Linearjeph\Prophecy;

/** Tiny HTTP transport: ext-curl when loaded, stream contexts otherwise; gzip bodies when ext-zlib is loaded. */
final class Http
{
    /** @return array{0:int,1:string,2:array<string,string>} status, body, lower-cased response headers; status 0 = network error */
    public static function send(string $method, string $url, array $headers, ?string $body, float $timeout): array
    {
        if ($body !== null && function_exists('gzencode')) {
            $gz = gzencode($body, 6);
            if ($gz !== false) {
                $body = $gz;
                $headers['Content-Encoding'] = 'gzip';
            }
        }
        if ($body !== null) {
            $headers['Content-Type'] = 'application/json';
        }
        $lines = [];
        foreach ($headers as $k => $v) {
            $lines[] = $k . ': ' . $v;
        }
        return function_exists('curl_init')
            ? self::curl($method, $url, $lines, $body, $timeout)
            : self::stream($method, $url, $lines, $body, $timeout);
    }

    private static function curl(string $method, string $url, array $lines, ?string $body, float $timeout): array
    {
        $ch = curl_init($url);
        $respHeaders = [];
        curl_setopt_array($ch, [
            CURLOPT_CUSTOMREQUEST => $method,
            CURLOPT_HTTPHEADER => $lines,
            CURLOPT_RETURNTRANSFER => true,
            CURLOPT_TIMEOUT_MS => (int) ($timeout * 1000),
            CURLOPT_CONNECTTIMEOUT_MS => (int) min($timeout, 5) * 1000,
            CURLOPT_HEADERFUNCTION => static function ($c, string $line) use (&$respHeaders): int {
                $p = explode(':', $line, 2);
                if (count($p) === 2) {
                    $respHeaders[strtolower(trim($p[0]))] = trim($p[1]);
                }
                return strlen($line);
            },
        ]);
        if ($body !== null) {
            curl_setopt($ch, CURLOPT_POSTFIELDS, $body);
        }
        $out = curl_exec($ch);
        $status = (int) curl_getinfo($ch, CURLINFO_RESPONSE_CODE);
        if ($out === false) {
            $err = curl_error($ch);
            curl_close($ch);
            return [0, $err, []];
        }
        curl_close($ch);
        return [$status, (string) $out, $respHeaders];
    }

    private static function stream(string $method, string $url, array $lines, ?string $body, float $timeout): array
    {
        $opts = ['http' => [
            'method' => $method,
            'header' => implode("\r\n", $lines),
            'timeout' => $timeout,
            'ignore_errors' => true,
        ]];
        if ($body !== null) {
            $opts['http']['content'] = $body;
        }
        $out = @file_get_contents($url, false, stream_context_create($opts));
        $status = 0;
        $respHeaders = [];
        // $http_response_header is populated in the local scope by file_get_contents
        foreach ($http_response_header ?? [] as $h) {
            if (preg_match('~^HTTP/\S+\s+(\d{3})~', $h, $m)) {
                $status = (int) $m[1];
            } elseif (str_contains($h, ':')) {
                [$k, $v] = explode(':', $h, 2);
                $respHeaders[strtolower(trim($k))] = trim($v);
            }
        }
        if ($out === false) {
            return [0, 'stream request failed', []];
        }
        return [$status, $out, $respHeaders];
    }
}
