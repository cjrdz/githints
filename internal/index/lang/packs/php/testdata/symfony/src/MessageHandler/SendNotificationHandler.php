<?php

namespace App\MessageHandler;

use Symfony\Component\Messenger\Attribute\AsMessageHandler;

#[AsMessageHandler]
final class SendNotificationHandler
{
    public function __invoke(object $message): void
    {
    }
}
