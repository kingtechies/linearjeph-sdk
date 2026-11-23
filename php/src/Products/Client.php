<?php
declare(strict_types=1);

namespace LinearJeph\Products;

/**
 * LINEARJEPH product API client: fix, shield, ops, models, agents, comply.
 *
 *   $lj = new Client();                       // LJ_API_KEY and LJ_BASE_URL from the environment
 *   $job = $lj->fix->wait($lj->fix->createJob(['source_id' => 'gh:acme/app', 'issue' => 'bug'])['id']);
 *
 * GET requests retry 429, 5xx and network errors (3 tries, honours Retry-After); writes are never retried.
 */
final class Client
{
    public const VERSION = '1.3.0';
    public const DEFAULT_BASE_URL = 'https://linearjeph.cc';

    public readonly Fix $fix;
    public readonly Shield $shield;
    public readonly Ops $ops;
    public readonly Models $models;
    public readonly Agents $agents;
    public readonly Comply $comply;

    private string $apiKey;
    private string $baseUrl;
    /** @var callable|null */
    private $transport;

    /**
     * @param callable|null $transport fn(string $method, string $url, array $headers, ?string $body, float $timeout): array{0:int,1:string,2:array<string,string>}
     *                                 replaces the built-in curl/stream transport (tests, proxies)
     */
    public function __construct(
        ?string $apiKey = null,
        ?string $baseUrl = null,
        private readonly ?string $org = null,
        private readonly float $timeout = 30.0,
        private readonly int $retries = 2,
        private readonly int $backoffMs = 500,
        public readonly float $pollInterval = 3.0,
        ?callable $transport = null,
    ) {
        $key = trim($apiKey ?? (string) getenv('LJ_API_KEY'));
        if ($key === '') {
            throw new \InvalidArgumentException('apiKey (or LJ_API_KEY) is required');
        }
        $this->apiKey = $key;
        $base = trim($baseUrl ?? (string) getenv('LJ_BASE_URL'));
        $this->baseUrl = rtrim($base === '' ? self::DEFAULT_BASE_URL : $base, '/');
        $this->transport = $transport;
        $this->fix = new Fix($this);
        $this->shield = new Shield($this);
        $this->ops = new Ops($this);
        $this->models = new Models($this);
        $this->agents = new Agents($this);
        $this->comply = new Comply($this);
    }

    /**
     * Calls any route. Answers decode to an array: [] for an empty body, ['data' => ...] for a JSON scalar or list,
     * ['raw' => '...'] for non-JSON text. Lists of records come back under 'results'.
     *
     * @param array<string,mixed>|null $body
     * @param array<string,scalar|null>|null $query
     * @return array<string,mixed>
     * @throws ApiError
     */
    public function request(string $method, string $path, ?array $body = null, ?array $query = null): array
    {
        $resp = $this->send(strtoupper($method), $path, $body, $query);
        if (trim($resp) === '') {
            return [];
        }
        $data = json_decode($resp, true);
        if (json_last_error() !== JSON_ERROR_NONE) {
            return ['raw' => $resp];
        }
        return is_array($data) && ($data === [] || !array_is_list($data)) ? $data : ['data' => $data];
    }

    /** Raw GET body, for patches and the compliance export zip. @throws ApiError */
    public function download(string $path, ?array $query = null): string
    {
        return $this->send('GET', $path, null, $query);
    }

    /** Organization, scopes and key details of the API key. @return array<string,mixed> */
    public function whoami(): array
    {
        return $this->request('GET', '/api/v1/apikeys/whoami');
    }

    /** @internal */
    public function get(string $path, ?array $query = null): array
    {
        return $this->request('GET', $path, null, $query);
    }

    /** @internal */
    public function post(string $path, ?array $body = null): array
    {
        return $this->request('POST', $path, $body);
    }

    /** Polls $fn until it returns [row, true] or the timeout passes. @throws \RuntimeException on timeout @internal */
    public function pollUntil(float $timeout, callable $fn): array
    {
        $deadline = microtime(true) + ($timeout > 0 ? $timeout : 1800.0);
        while (true) {
            [$row, $done] = $fn();
            if ($done) {
                return $row;
            }
            if (microtime(true) + $this->pollInterval > $deadline) {
                throw new \RuntimeException('Timed out waiting.');
            }
            usleep((int) ($this->pollInterval * 1_000_000));
        }
    }

    /** @throws ApiError */
    private function send(string $method, string $path, ?array $body, ?array $query): string
    {
        $query = $query ?? [];
        if ($this->org !== null && $this->org !== '' && !isset($query['org'])) {
            $query['org'] = $this->org;
        }
        $query = array_filter($query, static fn($v) => $v !== null);
        $url = $this->baseUrl . '/' . ltrim($path, '/') . ($query ? '?' . http_build_query($query) : '');
        $json = null;
        if ($body !== null) {
            $json = json_encode($body, JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_INVALID_UTF8_SUBSTITUTE | JSON_PRESERVE_ZERO_FRACTION);
            if ($json === false) {
                throw new ApiError(0, 'encode_error', 'Could not encode the request: ' . json_last_error_msg());
            }
        }
        $headers = [
            'Authorization' => 'Bearer ' . $this->apiKey,
            'User-Agent' => 'linearjeph-php/' . self::VERSION,
            'Accept' => 'application/json',
        ];
        if ($json !== null) {
            $headers['Content-Type'] = 'application/json';
        }
        $tries = $method === 'GET' ? 1 + max(0, $this->retries) : 1;
        $last = null;
        for ($attempt = 0; $attempt < $tries; $attempt++) {
            if ($attempt > 0) {
                $wait = $this->backoffMs * (2 ** ($attempt - 1));
                $wait += random_int(0, intdiv($wait, 4));
                if ($last instanceof ApiError && $last->retryAfter() > 0) {
                    $wait = min($last->retryAfter(), 30) * 1000;
                }
                usleep($wait * 1000);
            }
            [$status, $resp, $rh] = $this->transport !== null
                ? ($this->transport)($method, $url, $headers, $json, $this->timeout)
                : Transport::send($method, $url, $headers, $json, $this->timeout);
            if ($status >= 200 && $status < 300) {
                return $resp;
            }
            $last = self::error($status, $resp, (int) ($rh['retry-after'] ?? 0));
            if ($status !== 0 && $status !== 429 && $status < 500) {
                throw $last;
            }
        }
        throw $last ?? new ApiError(0, 'network_error', 'Request failed.');
    }

    private static function error(int $status, string $resp, int $retryAfter): ApiError
    {
        if ($status === 0) {
            return new ApiError(0, 'network_error', 'Network error: ' . $resp);
        }
        $code = 'http_' . $status;
        $msg = 'HTTP ' . $status;
        $data = json_decode($resp, true);
        $body = is_array($data) ? $data : [];
        if (isset($body['error']) && is_array($body['error'])) {
            $code = (string) ($body['error']['code'] ?? $code);
            $msg = (string) ($body['error']['message'] ?? $msg);
        }
        return new ApiError($status, $code, $msg, $body, $retryAfter);
    }
}
