<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Zimam's three fail2ban jails (R06-D5), as fail2ban.config last applied
 * them. Times are seconds, as the agent takes them.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('fail2ban_jails', function (Blueprint $table) {
            $table->id();
            $table->string('name', 24)->unique();
            $table->boolean('enabled');
            $table->unsignedInteger('max_retry');
            $table->unsignedInteger('find_time');
            $table->unsignedInteger('ban_time');
            $table->timestamps();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('fail2ban_jails');
    }
};
