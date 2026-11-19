<?php
declare(strict_types=1);

namespace Linearjeph\Prophecy;

/**
 * prophecy scan [dir] [--send]
 * prophecy import <events|customers> <file.csv|file.json>
 * Keys: LINEARJEPH_INGEST_KEY, LINEARJEPH_SECRET_KEY, LINEARJEPH_ENDPOINT.
 */
final class Cli
{
    private const TOP = ['ts', 'uid', 'name', 'kind', 'feature', 'amount', 'currency', 'id', 'signup_at', 'plan', 'mrr', 'country', 'channel'];

    /** @param list<string> $args arguments after the script name */
    public static function run(array $args, $out = null, $err = null): int
    {
        $out ??= STDOUT;
        $err ??= STDERR;
        $cmd = $args[0] ?? '';
        if ($cmd === 'scan') {
            $dir = '.';
            $send = false;
            foreach (array_slice($args, 1) as $a) {
                if ($a === '--send') {
                    $send = true;
                } else {
                    $dir = $a;
                }
            }
            try {
                $m = (new Scanner())->scan($dir);
                if (!$send) {
                    fwrite($out, json_encode($m, JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_INVALID_UTF8_SUBSTITUTE) . "\n");
                    return 0;
                }
                self::client()->sendManifest($m);
                $files = array_sum((array) $m['languages']);
                fwrite($out, sprintf("manifest sent: %d files in %d languages, %d routes, %d dependencies\n", $files, count((array) $m['languages']), count($m['routes']), count($m['dependencies'])));
                return 0;
            } catch (\Throwable $e) {
                fwrite($err, ($send ? 'send failed: ' : 'scan failed: ') . $e->getMessage() . "\n");
                return 1;
            }
        }
        if ($cmd === 'import' && count($args) === 3) {
            try {
                $rows = self::loadRows($args[2], $args[1]);
                $res = self::client()->importHistory($args[1], $rows, static function (int $d, int $t) use ($err): void {
                    fwrite($err, "imported $d/$t\n");
                });
                fwrite($out, "accepted {$res['accepted']}, rejected {$res['rejected']}\n");
                foreach ($res['errors'] as $e) {
                    fwrite($err, "  $e\n");
                }
                return 0;
            } catch (\Throwable $e) {
                fwrite($err, 'import failed: ' . $e->getMessage() . "\n");
                return 1;
            }
        }
        fwrite($err, "usage: prophecy scan [dir] [--send]\n       prophecy import <events|customers> <file.csv|file.json>\n");
        return 2;
    }

    private static function client(): Client
    {
        return new Client([
            'ingestKey' => (string) getenv('LINEARJEPH_INGEST_KEY'),
            'secretKey' => (string) getenv('LINEARJEPH_SECRET_KEY'),
            'endpoint' => getenv('LINEARJEPH_ENDPOINT') ?: Client::DEFAULT_ENDPOINT,
            'registerShutdown' => false,
        ]);
    }

    /** @return list<array<string,mixed>> */
    public static function loadRows(string $path, string $kind): array
    {
        if (!is_file($path)) {
            throw new ProphecyException("file not found: $path");
        }
        if (str_ends_with(strtolower($path), '.json')) {
            $raw = json_decode((string) file_get_contents($path), true);
            $list = is_array($raw) && array_is_list($raw) ? $raw : ($raw['rows'] ?? null);
            if (!is_array($list)) {
                throw new ProphecyException('expected a JSON array of objects (or {"rows": [...]})');
            }
            return array_values(array_filter($list, 'is_array'));
        }
        $fh = fopen($path, 'rb');
        $head = fgetcsv($fh, 0, ',', '"', '');
        if (!$head) {
            fclose($fh);
            return [];
        }
        $head = array_map('trim', $head);
        $rows = [];
        while (($rec = fgetcsv($fh, 0, ',', '"', '')) !== false) {
            if ($rec === [null]) {
                continue;
            }
            $row = [];
            $props = [];
            foreach ($head as $i => $h) {
                $v = $rec[$i] ?? '';
                if ($v === '') {
                    continue;
                }
                if ($h === 'amount' || $h === 'mrr') {
                    if (is_numeric($v)) {
                        $row[$h] = $v + 0;
                    }
                } elseif (str_starts_with($h, 'props.')) {
                    $props[substr($h, 6)] = $v;
                } elseif (in_array($h, self::TOP, true)) {
                    $row[$h] = $v;
                } else {
                    $props[$h] = $v;
                }
            }
            if ($props) {
                $row[$kind === 'customers' ? 'traits' : 'props'] = $props;
            }
            $rows[] = $row;
        }
        fclose($fh);
        return $rows;
    }
}
