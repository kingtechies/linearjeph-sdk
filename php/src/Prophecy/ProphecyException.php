<?php
declare(strict_types=1);

namespace Linearjeph\Prophecy;

/** Thrown only by the explicit, request/response style calls (report, refresh, live, embedUrl, importHistory, sendManifest). */
final class ProphecyException extends \RuntimeException
{
    public function __construct(string $message, private readonly int $status = 0, int $code = 0, ?\Throwable $previous = null)
    {
        parent::__construct($message, $code, $previous);
    }

    public function status(): int
    {
        return $this->status;
    }
}
