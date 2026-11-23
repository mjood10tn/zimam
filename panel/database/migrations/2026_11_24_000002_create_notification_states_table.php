<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * One row per event that notified (R05-D12): a warning notifies when it
 * starts and when it clears, and again every 24 hours while it lasts; any
 * other event notifies once.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('notification_states', function (Blueprint $table) {
            $table->id();
            // warning:<key>, agent:unreachable, job-failed:<id>, backup:<plan>:<id>, …
            $table->string('event_key')->unique();
            $table->timestamp('started_at');
            $table->timestamp('last_sent_at');
            $table->timestamp('cleared_at')->nullable();
            $table->timestamps();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('notification_states');
    }
};
