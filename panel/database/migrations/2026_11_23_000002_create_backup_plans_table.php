<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Backup plans (v0.5.0): what to back up, when, how many to keep and where
 * to. The panel's editing copy of what backup.plan applies; the agent's
 * plans.json is the truth on the server. A site or database that goes
 * leaves its plans through the pivots' cascades, as the agent forgets it
 * (R05-D13).
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('backup_plans', function (Blueprint $table) {
            $table->id();
            $table->string('key', 24)->unique();
            $table->foreignId('backup_destination_id')->constrained()->restrictOnDelete();
            // hourly, daily HH:MM or weekly <day> HH:MM, in the server's zone (R05-D7).
            $table->string('schedule', 32);
            // keep_last, keep_daily, keep_weekly, keep_monthly (R05-D8).
            $table->json('retention');
            $table->boolean('include_panel')->default(false);
            // saving, active, failed or deleting.
            $table->string('status');
            $table->text('last_error')->nullable();
            $table->timestamps();
        });
        Schema::create('backup_plan_site', function (Blueprint $table) {
            $table->foreignId('backup_plan_id')->constrained()->cascadeOnDelete();
            $table->foreignId('site_id')->constrained()->cascadeOnDelete();
            $table->primary(['backup_plan_id', 'site_id']);
        });
        Schema::create('backup_plan_database', function (Blueprint $table) {
            $table->foreignId('backup_plan_id')->constrained()->cascadeOnDelete();
            $table->foreignId('database_id')->constrained()->cascadeOnDelete();
            $table->primary(['backup_plan_id', 'database_id']);
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('backup_plan_database');
        Schema::dropIfExists('backup_plan_site');
        Schema::dropIfExists('backup_plans');
    }
};
