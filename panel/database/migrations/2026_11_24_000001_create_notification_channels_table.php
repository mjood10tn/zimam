<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Notification channels (v0.5.0, R05-D11): email, Telegram, Slack or
 * Discord, and the events each one gets. The settings hold tokens and
 * webhook URLs, so they are stored with Laravel's encrypted cast.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('notification_channels', function (Blueprint $table) {
            $table->id();
            // email, telegram, slack or discord.
            $table->string('type');
            $table->string('name', 64);
            // Encrypted JSON: to; bot_token and chat_id; or webhook_url.
            $table->text('settings');
            $table->json('events');
            $table->boolean('enabled')->default(true);
            $table->text('last_error')->nullable();
            $table->timestamp('last_sent_at')->nullable();
            $table->timestamps();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('notification_channels');
    }
};
