<?php
declare(strict_types=1);

namespace Linearjeph\Prophecy;

/**
 * Prophecy client. Tracking calls buffer in memory and never throw; the buffer is sent on flush(), when it reaches
 * batchSize (flushAt) and at the end of the request (register_shutdown_function, after fastcgi_finish_request when
 * available so the visitor never waits). Secret-key calls throw ProphecyException on failure.
 */
final class Client
{
    public const VERSION = '1.2.0';
    public const DEFAULT_ENDPOINT = 'https://linearjeph.cc';
    private const KINDS = ['signup', 'login', 'active', 'feature', 'payment', 'refund', 'subscription', 'churn', 'pageview', 'request', 'error', 'custom'];

    private string $ingestKey;
    private string $secretKey;
    private string $endpoint;
    private bool $hash;
    private int $queueSize;
    private int $batchSize;
    private int $flushAt;
    private int $retries;
    private int $backoffMs;
    private float $timeout;
    private bool $debug;
    /** @var list<array<string,mixed>> */
    private array $events = [];
    /** @var list<array<string,mixed>> */
    private array $customers = [];
    private int $dropped = 0;
    private bool $closed = false;
    private bool $flushing = false;
    /** @var array<string,bool> */
    private array $warned = [];

    /**
     * @param array{ingestKey?:string,secretKey?:string,endpoint?:string,hashUserIds?:bool,queueSize?:int,batchSize?:int,flushAt?:int,retries?:int,backoffMs?:int,timeout?:float,debug?:bool,registerShutdown?:bool} $options
     */
    public function __construct(array $options = [])
    {
        $this->ingestKey = trim((string) ($options['ingestKey'] ?? ''));
        $this->secretKey = trim((string) ($options['secretKey'] ?? ''));
        if ($this->ingestKey === '' && $this->secretKey === '') {
            throw new \InvalidArgumentException('Prophecy: ingestKey (or secretKey) is required');
        }
        $this->endpoint = rtrim((string) ($options['endpoint'] ?? self::DEFAULT_ENDPOINT), '/');
        $this->hash = (bool) ($options['hashUserIds'] ?? true);
        $this->queueSize = max(1, (int) ($options['queueSize'] ?? 10000));
        $this->batchSize = min(500, max(1, (int) ($options['batchSize'] ?? 100)));
        $this->flushAt = (int) ($options['flushAt'] ?? $this->batchSize);
        $this->retries = max(0, (int) ($options['retries'] ?? 4));
        $this->backoffMs = max(1, (int) ($options['backoffMs'] ?? 500));
        $this->timeout = max(1.0, (float) ($options['timeout'] ?? 10));
        $this->debug = (bool) ($options['debug'] ?? false);
        if ($options['registerShutdown'] ?? true) {
            register_shutdown_function([$this, 'flushAfterResponse']);
        }
    }

    /** Truncate to $n characters (multibyte aware when ext-mbstring is loaded). */
    private static function clip(string $s, int $n): string
    {
        return function_exists('mb_substr') ? mb_substr($s, 0, $n) : substr($s, 0, $n);
    }

    public static function hashUid(string $uid): string
    {
        return hash('sha256', $uid);
    }

    public function dropped(): int
    {
        return $this->dropped;
    }

    // ---- tracking (never throw) ---------------------------------------------------------------------------

    /** @param array{uid?:string|int|null,kind?:string,feature?:string,amount?:int|float,currency?:string,props?:array,ts?:int|float|string|\DateTimeInterface} $o */
    public function track(string $name, array $o = []): void
    {
        $this->safe(function () use ($name, $o): void {
            $name = trim($name);
            if ($name === '') {
                return;
            }
            $kind = $o['kind'] ?? (isset($o['feature']) && $o['feature'] !== '' ? 'feature' : 'custom');
            if (!in_array($kind, self::KINDS, true)) {
                $kind = 'custom';
            }
            $ev = ['ts' => $this->iso($o['ts'] ?? null), 'name' => self::clip($name, 80), 'kind' => $kind];
            $uid = $this->uid($o['uid'] ?? null);
            if ($uid !== '') {
                $ev['uid'] = $uid;
            }
            if (!empty($o['feature'])) {
                $ev['feature'] = self::clip((string) $o['feature'], 80);
            }
            if (isset($o['amount']) && is_numeric($o['amount'])) {
                $ev['amount'] = $o['amount'] + 0;
            }
            if (!empty($o['currency'])) {
                $ev['currency'] = strtoupper(substr((string) $o['currency'], 0, 3));
            }
            $props = $this->cleanProps($o['props'] ?? null);
            if ($props !== null) {
                $ev['props'] = $props;
            }
            $this->enqueue($ev);
        });
    }

    public function feature(string $name, string|int|null $uid = null): void
    {
        $this->track($name, ['kind' => 'feature', 'feature' => $name, 'uid' => $uid]);
    }

    /** @param array{plan?:string,channel?:string,country?:string,ts?:mixed} $o */
    public function signup(string|int $uid, array $o = []): void
    {
        $this->safe(function () use ($uid, $o): void {
            $p = $this->strip(['plan' => $o['plan'] ?? null, 'channel' => $o['channel'] ?? null, 'country' => $o['country'] ?? null]);
            $this->track('signup', ['kind' => 'signup', 'uid' => $uid, 'ts' => $o['ts'] ?? null, 'props' => $p]);
            $this->identify($uid, $p + ['signup_at' => $this->iso($o['ts'] ?? null)]);
        });
    }

    /** @param array{plan?:string,currency?:string,ts?:mixed} $o a negative amount is recorded as a refund */
    public function payment(string|int $uid, int|float $amount, ?string $currency = null, array $o = []): void
    {
        $kind = $amount < 0 ? 'refund' : 'payment';
        $this->track($kind, [
            'kind' => $kind, 'uid' => $uid, 'amount' => $amount, 'currency' => $currency ?? ($o['currency'] ?? 'USD'),
            'ts' => $o['ts'] ?? null, 'props' => $this->strip(['plan' => $o['plan'] ?? null]),
        ]);
    }

    /** @param array{plan?:string,mrr?:int|float,status?:string,ts?:mixed} $o status: active|cancelled|paused */
    public function subscription(string|int $uid, array $o = []): void
    {
        $this->safe(function () use ($uid, $o): void {
            $p = $this->strip(['plan' => $o['plan'] ?? null, 'mrr' => isset($o['mrr']) ? $o['mrr'] + 0 : null, 'status' => $o['status'] ?? 'active']);
            $this->track('subscription', ['kind' => 'subscription', 'uid' => $uid, 'ts' => $o['ts'] ?? null, 'props' => $p]);
            $this->identify($uid, $this->strip(['plan' => $p['plan'] ?? null, 'mrr' => $p['mrr'] ?? null]));
        });
    }

    public function churn(string|int $uid, ?string $reason = null): void
    {
        $this->track('churn', ['kind' => 'churn', 'uid' => $uid, 'props' => $reason ? ['reason' => self::clip($reason, 200)] : null]);
    }

    /** plan, mrr, country, channel and signup_at become customer fields; other keys (max 20) are traits. */
    public function identify(string|int $uid, array $traits = []): void
    {
        $this->safe(function () use ($uid, $traits): void {
            $u = $this->uid($uid);
            if ($u === '') {
                return;
            }
            $c = ['uid' => $u];
            $rest = [];
            foreach ($traits as $k => $v) {
                $k = (string) $k;
                if ($k === 'signupAt') {
                    $k = 'signup_at';
                }
                if (in_array($k, ['plan', 'mrr', 'country', 'channel', 'signup_at'], true)) {
                    if ($v !== null && $v !== '') {
                        $c[$k] = $v;
                    }
                } elseif (count($rest) < 20) {
                    $rest[$k] = $v;
                }
            }
            $rest = $this->cleanProps($rest);
            if ($rest) {
                $c['traits'] = $rest;
            }
            $this->enqueueCustomer($c);
        });
    }

    /** Record one handled request: route template, method, HTTP status, duration in milliseconds. No bodies, no query. */
    public function recordRequest(string $route, string $method, int $status, float $durationMs): void
    {
        $this->track('request', ['kind' => 'request', 'props' => [
            'route' => self::clip($route, 200), 'method' => strtoupper(substr($method, 0, 10)),
            'status' => $status, 'duration_ms' => round($durationMs, 1),
        ]]);
    }

    // ---- delivery -----------------------------------------------------------------------------------------

    /** Send everything buffered. Returns true when the buffer is empty afterwards. Never throws. */
    public function flush(): bool
    {
        if ($this->flushing) {
            return false;
        }
        $this->flushing = true;
        try {
            while ($this->events || $this->customers) {
                $cu = array_splice($this->customers, 0, $this->batchSize);
                $ev = array_splice($this->events, 0, $this->batchSize);
                if ($cu) {
                    $this->deliver('/ingest/v1/prophecy/identify', ['customers' => $cu]);
                }
                if ($ev) {
                    $this->deliver('/ingest/v1/prophecy/events', ['events' => $ev]);
                }
            }
        } catch (\Throwable $e) {
            $this->warn('flush', 'flush error: ' . $e->getMessage());
        } finally {
            $this->flushing = false;
        }
        return !$this->events && !$this->customers;
    }

    /** Shutdown hook: release the visitor first (FastCGI / LiteSpeed), then flush. */
    public function flushAfterResponse(): void
    {
        $this->safe(function (): void {
            if (!$this->events && !$this->customers) {
                return;
            }
            if (function_exists('fastcgi_finish_request')) {
                @fastcgi_finish_request();
            } elseif (function_exists('litespeed_finish_request')) {
                @litespeed_finish_request();
            }
            $this->flush();
        });
    }

    public function close(): bool
    {
        $ok = $this->flush();
        $this->closed = true;
        return $ok;
    }

    private function enqueue(array $ev): void
    {
        if ($this->closed || $this->ingestKey === '' || count($this->events) >= $this->queueSize) {
            $this->dropped++;
            return;
        }
        $this->events[] = $ev;
        if ($this->flushAt > 0 && count($this->events) >= $this->flushAt) {
            $this->flush();
        }
    }

    private function enqueueCustomer(array $c): void
    {
        if ($this->closed || $this->ingestKey === '' || count($this->customers) >= $this->queueSize) {
            $this->dropped++;
            return;
        }
        $this->customers[] = $c;
    }

    private function deliver(string $path, array $body): void
    {
        try {
            $this->request('POST', $path, $body, false);
        } catch (\Throwable $e) {
            $this->warn('deliver', "delivery to $path failed: " . $e->getMessage());
        }
    }

    /**
     * @return array{0:int,1:string} status and body. Retries network errors, 429 and 5xx with exponential backoff.
     * @throws ProphecyException when every attempt failed or the server answered 4xx
     */
    private function request(string $method, string $path, ?array $body, bool $secret): array
    {
        $json = $body === null ? null : json_encode($body, JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_INVALID_UTF8_SUBSTITUTE | JSON_PRESERVE_ZERO_FRACTION);
        if ($body !== null && $json === false) {
            throw new ProphecyException('could not encode request: ' . json_last_error_msg());
        }
        $headers = [
            'User-Agent' => 'linearjeph-prophecy-php/' . self::VERSION,
            'Accept' => 'application/json',
        ];
        if ($secret) {
            $headers['Authorization'] = 'Bearer ' . $this->secretKey;
        } else {
            $headers['X-LJ-Key'] = $this->ingestKey;
        }
        $last = null;
        for ($attempt = 0; $attempt <= $this->retries; $attempt++) {
            if ($attempt > 0) {
                $wait = $this->backoffMs * (2 ** ($attempt - 1));
                $wait += random_int(0, intdiv($wait, 2));
                if ($last instanceof ProphecyException && ($ra = $last->getCode()) > 0 && $ra <= 30) {
                    $wait = $ra * 1000;
                }
                usleep($wait * 1000);
            }
            [$status, $resp, $rh] = Http::send($method, $this->endpoint . $path, $headers, $json, $this->timeout);
            if ($status === 0) {
                $last = new ProphecyException('network error: ' . $resp);
                continue;
            }
            if ($status === 429 || $status >= 500) {
                $last = new ProphecyException("HTTP $status " . substr($resp, 0, 200), $status, (int) ($rh['retry-after'] ?? 0));
                continue;
            }
            if ($status >= 400) {
                throw new ProphecyException("HTTP $status " . substr($resp, 0, 200), $status);
            }
            return [$status, $resp];
        }
        throw $last ?? new ProphecyException('request failed');
    }

    // ---- manifest + secret-key API (throw on failure) ---------------------------------------------------------

    public function sendManifest(array $manifest): void
    {
        if ($this->ingestKey === '') {
            throw new ProphecyException('ingestKey is required to send a manifest');
        }
        $this->request('POST', '/ingest/v1/prophecy/manifest', ['manifest' => $manifest], false);
    }

    private function secret(string $method, string $path, ?array $body = null): array
    {
        if ($this->secretKey === '') {
            throw new ProphecyException('secretKey is required for this call (server side only)');
        }
        [, $resp] = $this->request($method, $path, $body, true);
        $data = json_decode($resp, true);
        return is_array($data) ? $data : [];
    }

    /** Latest report: ['report' => ..., 'created_at' => ..., 'stale' => bool]; $section narrows it (e.g. 'revenue'). */
    public function report(?string $section = null): array
    {
        return $this->secret('GET', '/api/v1/prophecy/sdk/report' . ($section ? '?section=' . rawurlencode($section) : ''));
    }

    public function refresh(): array
    {
        return $this->secret('POST', '/api/v1/prophecy/sdk/refresh', []);
    }

    public function live(): array
    {
        return $this->secret('GET', '/api/v1/prophecy/sdk/live');
    }

    public function embedUrl(int $ttl = 3600): string
    {
        $r = $this->secret('POST', '/api/v1/prophecy/sdk/embed', ['ttl' => min(86400, max(60, $ttl))]);
        if (empty($r['url'])) {
            throw new ProphecyException('no url in response');
        }
        return (string) $r['url'];
    }

    /**
     * Backfill history in batches of 5,000 rows / 5 MB with retries. $kind is 'events' or 'customers'; rows use the ingest
     * shape (optional "id" dedupes). uids are hashed like live events. $progress(done, total) is called after each batch.
     *
     * @return array{accepted:int,rejected:int,errors:list<string>,jobs:list<mixed>}
     */
    public function importHistory(string $kind, iterable $rows, ?callable $progress = null): array
    {
        if (!in_array($kind, ['events', 'customers'], true)) {
            throw new ProphecyException('kind must be "events" or "customers"');
        }
        if ($this->secretKey === '') {
            throw new ProphecyException('secretKey is required for importHistory');
        }
        $all = [];
        foreach ($rows as $r) {
            $r = (array) $r;
            if (isset($r['uid']) && $r['uid'] !== '') {
                $r['uid'] = $this->uid($r['uid']);
            }
            $all[] = $r;
        }
        $total = count($all);
        $res = ['accepted' => 0, 'rejected' => 0, 'errors' => [], 'jobs' => []];
        $done = 0;
        $post = function (array $batch) use (&$post, $kind, &$res, &$done, $total, $progress): void {
            $raw = json_encode($batch, JSON_INVALID_UTF8_SUBSTITUTE);
            if (count($batch) > 1 && $raw !== false && strlen($raw) > 4500 * 1024) {
                $h = intdiv(count($batch), 2);
                $post(array_slice($batch, 0, $h));
                $post(array_slice($batch, $h));
                return;
            }
            $out = $this->secret('POST', '/api/v1/prophecy/sdk/import', ['kind' => $kind, 'rows' => $batch]);
            $res['accepted'] += (int) ($out['accepted'] ?? 0);
            $res['rejected'] += (int) ($out['rejected'] ?? 0);
            if (isset($out['job'])) {
                $res['jobs'][] = $out['job'];
            }
            foreach ((array) ($out['errors'] ?? []) as $e) {
                if (count($res['errors']) < 20) {
                    $res['errors'][] = is_string($e) ? $e : (string) json_encode($e);
                }
            }
            $done += count($batch);
            if ($progress) {
                try {
                    $progress($done, $total);
                } catch (\Throwable $e) {
                    $this->warn('progress', 'progress callback failed: ' . $e->getMessage());
                }
            }
        };
        foreach (array_chunk($all, 5000) as $batch) {
            $post($batch);
        }
        return $res;
    }

    // ---- helpers ----------------------------------------------------------------------------------------------

    private function uid(string|int|null $uid): string
    {
        if ($uid === null || $uid === '') {
            return '';
        }
        $u = (string) $uid;
        return $this->hash ? self::hashUid($u) : self::clip($u, 200);
    }

    private function iso(mixed $ts): string
    {
        if ($ts instanceof \DateTimeInterface) {
            $d = \DateTimeImmutable::createFromInterface($ts);
        } elseif (is_int($ts) || is_float($ts)) {
            $d = new \DateTimeImmutable(sprintf('@%.3F', ($ts > 1e11 ? $ts / 1000 : $ts)));
        } elseif (is_string($ts) && $ts !== '') {
            try {
                $d = new \DateTimeImmutable($ts);
            } catch (\Throwable) {
                return $this->iso(null);
            }
        } else {
            $m = microtime(true);
            return gmdate('Y-m-d\TH:i:s', (int) $m) . sprintf('.%03dZ', (int) (($m - floor($m)) * 1000));
        }
        return $d->setTimezone(new \DateTimeZone('UTC'))->format('Y-m-d\TH:i:s.v\Z');
    }

    private function cleanProps(mixed $props): ?array
    {
        if (!is_array($props) || !$props) {
            return null;
        }
        $s = json_encode($props, JSON_INVALID_UTF8_SUBSTITUTE);
        if ($s === false || strlen($s) > 2048) {
            $this->warn('props', 'props dropped (not serialisable or over 2 KB)');
            return null;
        }
        return $props;
    }

    private function strip(array $a): array
    {
        return array_filter($a, static fn ($v) => $v !== null && $v !== '');
    }

    private function safe(callable $fn): void
    {
        try {
            $fn();
        } catch (\Throwable $e) {
            $this->warn('internal', 'ignored internal error: ' . $e->getMessage());
        }
    }

    private function warn(string $key, string $msg): void
    {
        if (isset($this->warned[$key]) && !$this->debug) {
            return;
        }
        $this->warned[$key] = true;
        error_log('[prophecy] ' . $msg);
    }
}
