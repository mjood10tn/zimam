<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Each site's cron jobs as last saved from the Schedule tab (v0.4.0, R04-D5):
 * the panel's editing copy of the list site.cron applies. The agent's state
 * file keeps the jobs without their commands (R04-D16), so the command is
 * kept here; it isn't a secret.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('site_cron_jobs', function (Blueprint $table) {
            $table->id();
            $table->foreignId('site_id')->constrained()->cascadeOnDelete();
            $table->string('job_id', 16);
            // As the owner wrote it; the agent normalises it.
            $table->string('schedule', 200);
            $table->text('command');
            $table->boolean('enabled')->default(true);
            // applying, removing, active or failed.
            $table->string('status');
            $table->timestamps();
            $table->unique(['site_id', 'job_id']);
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('site_cron_jobs');
    }
};
