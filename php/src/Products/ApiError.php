<?php
declare(strict_types=1);

namespace LinearJeph\Products;

/** Every non-2xx answer, built from {"error": {"code", "message"}}. status 0 = network error. */
final class ApiError extends \RuntimeException
{
    /** @param array<string,mixed> $body */
    public function __construct(
        private readonly int $status,
        private readonly string $errorCode,
        string $message,
        private readonly array $body = [],
        private readonly int $retryAfter = 0,
    ) {
        parent::__construct($message, $status);
    }

    public function status(): int
    {
        return $this->status;
    }

    /** The machine readable code, such as "not_found" or "not_entitled". */
    public function errorCode(): string
    {
        return $this->errorCode;
    }

    /** @return array<string,mixed> */
    public function body(): array
    {
        return $this->body;
    }

    public function retryAfter(): int
    {
        return $this->retryAfter;
    }
}
