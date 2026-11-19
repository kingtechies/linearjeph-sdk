<?php
declare(strict_types=1);

namespace Linearjeph;

use Linearjeph\Prophecy\Client;
use Linearjeph\Prophecy\Middleware;
use Linearjeph\Prophecy\ProphecyException;
use Linearjeph\Prophecy\Scanner;

/**
 * Static facade over Linearjeph\Prophecy\Client.
 *
 *     Prophecy::init(['ingestKey' => getenv('LINEARJEPH_INGEST_KEY'), 'secretKey' => getenv('LINEARJEPH_SECRET_KEY')]);
 *     Prophecy::signup($user->id, ['plan' => 'free', 'channel' => 'ads']);
 *     Prophecy::payment($user->id, 49.0, 'USD', ['plan' => 'pro']);
 *
 * Tracking calls before init() are silent no-ops and never throw.
 */
final class Prophecy
{
    public const VERSION = Client::VERSION;

    private static ?Client $client = null;

    /** @param array<string,mixed> $options see Client::__construct (ingestKey, secretKey, endpoint, hashUserIds, ...) */
    public static function init(array $options): Client
    {
        return self::$client = new Client($options);
    }

    public static function client(): ?Client
    {
        return self::$client;
    }

    /** Replace the client (tests). */
    public static function setClient(?Client $client): void
    {
        self::$client = $client;
    }

    public static function track(string $name, array $o = []): void
    {
        self::$client?->track($name, $o);
    }

    public static function feature(string $name, string|int|null $uid = null): void
    {
        self::$client?->feature($name, $uid);
    }

    public static function signup(string|int $uid, array $o = []): void
    {
        self::$client?->signup($uid, $o);
    }

    public static function payment(string|int $uid, int|float $amount, ?string $currency = null, array $o = []): void
    {
        self::$client?->payment($uid, $amount, $currency, $o);
    }

    public static function subscription(string|int $uid, array $o = []): void
    {
        self::$client?->subscription($uid, $o);
    }

    public static function churn(string|int $uid, ?string $reason = null): void
    {
        self::$client?->churn($uid, $reason);
    }

    public static function identify(string|int $uid, array $traits = []): void
    {
        self::$client?->identify($uid, $traits);
    }

    public static function recordRequest(string $route, string $method, int $status, float $durationMs): void
    {
        self::$client?->recordRequest($route, $method, $status, $durationMs);
    }

    /** PSR-15 style middleware (duck typed: process($request, $handler)); also callable as ($request, $handler). */
    public static function middleware(): Middleware
    {
        return new Middleware();
    }

    public static function scan(string $root): array
    {
        return (new Scanner())->scan($root);
    }

    public static function sendManifest(array $manifest): void
    {
        self::need()->sendManifest($manifest);
    }

    public static function importHistory(string $kind, iterable $rows, ?callable $progress = null): array
    {
        return self::need()->importHistory($kind, $rows, $progress);
    }

    public static function report(?string $section = null): array
    {
        return self::need()->report($section);
    }

    public static function refresh(): array
    {
        return self::need()->refresh();
    }

    public static function live(): array
    {
        return self::need()->live();
    }

    public static function embedUrl(int $ttl = 3600): string
    {
        return self::need()->embedUrl($ttl);
    }

    public static function flush(): bool
    {
        return self::$client?->flush() ?? true;
    }

    public static function close(): bool
    {
        return self::$client?->close() ?? true;
    }

    private static function need(): Client
    {
        return self::$client ?? throw new ProphecyException('call Prophecy::init() first');
    }
}
